package primary

import (
	"context"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"time"

	app "quotes/internal/core/application/prices"
)

type paymentIdentity struct{ Contract, TokenID string }

type tokenMetadata struct {
	TokenID string            `json:"token_id"`
	Info    map[string]string `json:"token_info"`
}

type metadataUpdate struct {
	ID        int64          `json:"id"`
	Level     int64          `json:"level"`
	Timestamp time.Time      `json:"timestamp"`
	Action    string         `json:"action"`
	Value     *tokenMetadata `json:"value"`
}

type metadataVersion struct {
	Level     int64
	Timestamp time.Time
	Scale     *int
}

type metadataHistory []metadataVersion

type metadataTerms struct {
	scale *int
	from  metadataVersion
	until *metadataVersion
}

func (m metadataHistory) at(req app.PrimaryRequest) (metadataTerms, error) {
	index := -1
	for i, v := range m {
		if v.Level < req.Level {
			index = i
		}
		if v.Level == req.Level {
			return metadataTerms{}, app.PrimaryMissing("same_block_change")
		}
	}
	if index < 0 || m[index].Scale == nil {
		return metadataTerms{}, app.PrimaryMissing("payment_metadata_missing")
	}
	if m[index].Timestamp.After(req.At) {
		return metadataTerms{}, app.PrimaryMissing("block_time_mismatch")
	}
	t := metadataTerms{scale: m[index].Scale, from: m[index]}
	if index+1 < len(m) {
		t.until = &m[index+1]
		if !req.At.Before(t.until.Timestamp) {
			return metadataTerms{}, app.PrimaryMissing("block_time_mismatch")
		}
	}
	return t, nil
}

func metadataScale(value *tokenMetadata, tokenID string) *int {
	if value == nil || value.TokenID != tokenID {
		return nil
	}
	// TZIP-12 token_info bytes are hex-encoded by MvKT. Empty/malformed
	// decimals never inherit the conventional USDT scale of six.
	raw, ok := value.Info["decimals"]
	if !ok || len(raw) == 0 || len(raw) > 4 {
		return nil
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 || n > 40 || strconv.Itoa(n) != string(b) {
		return nil
	}
	return &n
}

func (h *History) loadPayments(ctx context.Context, launches []launchHistory, level int64, rows *int) (map[paymentIdentity]metadataHistory, error) {
	identities := map[paymentIdentity]bool{}
	for _, launch := range launches {
		for _, u := range launch.updates {
			if u.Value == nil {
				continue
			}
			// Only supported conditions need payment metadata. Their identity
			// comes from each historical launch version, not today's allowlist.
			option, found := u.Value.Options["Starter"]
			if !found || len(u.Value.Options) != 1 || len(option.Payments) != 1 {
				continue
			}
			pay, found := option.Payments["USDT"]
			if found && pay.Currency.FA2 != nil {
				identities[paymentIdentity{Contract: pay.Currency.FA2.Contract, TokenID: pay.Currency.FA2.TokenID}] = true
			}
		}
	}
	if len(identities) > 32 {
		return nil, app.PrimaryMissing("history_budget_exceeded")
	}
	result := map[paymentIdentity]metadataHistory{}
	for identity := range identities {
		versions, err := h.loadMetadata(ctx, identity, level, rows)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if *rows > historyRowLimit {
			return nil, app.PrimaryMissing("history_budget_exceeded")
		}
		// An unavailable old payment generation does not hide a different
		// generation whose on-chain metadata can be proved.
		if err == nil {
			result[identity] = versions
		}
	}
	return result, nil
}

func (h *History) loadMetadata(ctx context.Context, identity paymentIdentity, level int64, rows *int) (metadataHistory, error) {
	id, err := strconv.ParseUint(identity.TokenID, 10, 64)
	if !validContract(identity.Contract) || err != nil || strconv.FormatUint(id, 10) != identity.TokenID {
		return nil, app.PrimaryMissing("payment_metadata_missing")
	}
	var storage struct {
		Metadata int64 `json:"token_metadata"`
	}
	if err := h.get(ctx, "/contracts/"+identity.Contract+"/storage", &storage); err != nil {
		return nil, err
	}
	if storage.Metadata <= 0 {
		return nil, app.PrimaryMissing("payment_metadata_missing")
	}
	path := "/bigmaps/" + strconv.FormatInt(storage.Metadata, 10)
	var keys []struct {
		Hash string `json:"hash"`
		Key  string `json:"key"`
	}
	if err := h.get(ctx, path+"/keys?key="+url.QueryEscape(identity.TokenID)+"&limit=2", &keys); err != nil {
		return nil, err
	}
	if len(keys) != 1 || keys[0].Key != identity.TokenID || keys[0].Hash == "" {
		return nil, app.PrimaryMissing("payment_metadata_missing")
	}
	updates := []metadataUpdate{}
	lastID := int64(0)
	for offset := 0; ; offset += historyPageSize {
		var page []metadataUpdate
		query := url.Values{"limit": {strconv.Itoa(historyPageSize)}, "offset": {strconv.Itoa(offset)}, "sort.asc": {"id"}, "level.le": {strconv.FormatInt(level, 10)}}
		if err := h.get(ctx, path+"/keys/"+url.PathEscape(keys[0].Hash)+"/updates?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		*rows += len(page)
		if *rows > historyRowLimit {
			return nil, app.PrimaryMissing("history_budget_exceeded")
		}
		for _, u := range page {
			if u.ID <= lastID || u.Level <= 0 || u.Timestamp.IsZero() {
				return nil, app.PrimaryMissing("payment_metadata_missing")
			}
			lastID = u.ID
			if u.Level <= level {
				updates = append(updates, u)
			}
		}
		if len(page) < historyPageSize {
			break
		}
	}
	sort.Slice(updates, func(i, j int) bool {
		if updates[i].Level != updates[j].Level {
			return updates[i].Level < updates[j].Level
		}
		return updates[i].ID < updates[j].ID
	})
	if len(updates) == 0 || updates[0].Action != "add_key" {
		return nil, app.PrimaryMissing("payment_metadata_missing")
	}
	versions := metadataHistory{}
	for _, u := range updates {
		var scale *int
		switch u.Action {
		case "add_key", "update_key":
			scale = metadataScale(u.Value, identity.TokenID)
		case "remove_key":
		default:
			return nil, app.PrimaryMissing("payment_metadata_missing")
		}
		if len(versions) == 0 || !sameScale(versions[len(versions)-1].Scale, scale) {
			versions = append(versions, metadataVersion{Level: u.Level, Timestamp: u.Timestamp, Scale: scale})
		}
	}
	return versions, nil
}

func sameScale(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
