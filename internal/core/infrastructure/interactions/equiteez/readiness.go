package equiteez

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/graphql"
)

type IndexerStatus struct {
	Schemas []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"dipdup_schema"`
	Heads []struct {
		Timestamp time.Time `json:"timestamp"`
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"dipdup_head"`
	Indexes []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"dipdup_index"`
}

func (s IndexerStatus) ReadyGeneration(cfg config.EquiteezConfig, now time.Time) (string, error) {
	generation := ""
	for _, schema := range s.Schemas {
		if schema.Name == "public" && !schema.CreatedAt.IsZero() {
			generation = schema.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if generation == "" {
		return "", fmt.Errorf("indexer schema generation is unavailable")
	}
	if len(s.Heads) == 0 {
		return "", fmt.Errorf("indexer head is unavailable")
	}
	for _, head := range s.Heads {
		for _, stamp := range []time.Time{head.Timestamp, head.UpdatedAt} {
			age := now.Sub(stamp)
			if stamp.IsZero() || age > cfg.HeadMaxAge() || age < -time.Minute {
				return "", fmt.Errorf("indexer head is stale")
			}
		}
	}
	statuses := make(map[string]string, len(s.Indexes))
	for _, idx := range s.Indexes {
		statuses[idx.Name] = idx.Status
		if idx.Status != "realtime" {
			return "", fmt.Errorf("indexer %s is %s", idx.Name, idx.Status)
		}
	}
	for _, name := range cfg.ExpectedIndexes() {
		if statuses[name] != "realtime" {
			return "", fmt.Errorf("required index %s is not realtime", name)
		}
	}
	return generation, nil
}

func (c *Client) ReadyGeneration(ctx context.Context, cfg config.EquiteezConfig) (string, error) {
	const query = `query quotesIndexerStatus { dipdup_schema(where:{name:{_eq:"public"}}) { name created_at } dipdup_head { timestamp updated_at } dipdup_index { name status } }`
	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, nil, nil)
	if err != nil {
		return "", err
	}
	var status IndexerStatus
	if err = json.Unmarshal(data, &status); err != nil {
		return "", err
	}
	return status.ReadyGeneration(cfg, time.Now().UTC())
}

func (c *Client) CheckGeneration(ctx context.Context, cfg config.EquiteezConfig, expected string) error {
	generation, err := c.ReadyGeneration(ctx, cfg)
	if err != nil {
		return err
	}
	if generation != expected {
		return fmt.Errorf("indexer generation changed during request")
	}
	return nil
}
