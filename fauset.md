# Context

The current Mavryk faucet suffers from scalability issues under high user traffic, resulting in failed token distributions. Two faucet implementations exist today, each with different tradeoffs. This document proposes a new backend architecture that combines the best of both while adding abuse prevention features.

---

## Current Implementations

### Implementation A — React + Express

**Stack**: React SPA frontend, Express.js backend, Redis for PoW challenge state.

**How it works**: A user connects their wallet on the web UI, completes a CAPTCHA and/or Proof-of-Work challenges, then submits a request. The backend receives the request, verifies the PoW solution, submits a single blockchain transaction, waits for on-chain confirmation, and only then responds to the user.

**Strengths**:
- Full web dApp experience with wallet connect
- PoW + CAPTCHA anti-abuse system
- Configurable token amounts (min/max range)
- Familiar React + Node stack

**Weaknesses**:
- Synchronous and blocking: each HTTP request holds an open connection while waiting for chain confirmation
- Single faucet signer account: concurrent requests cause nonce conflicts and transaction failures
- No request queue: under load, requests pile up, timeout, or silently fail
- No retry logic: a failed RPC call is an immediate failure for the user
- No rate limiting at the API level
- No persistent record of requests (Redis is ephemeral)

### Implementation B — Laravel / Telegram (Tim’s faucet)

**Stack**: Laravel 12 backend, Blade + Tailwind frontend (minimal), Python Telegram bot for request ingestion, MySQL/SQLite for persistent storage, Laravel Horizon for queue monitoring.

**How it works**: Users post their `mv1...` address in a Telegram group. A Python bot scrapes messages every minute and saves requests to a database. A scheduled job picks up unprocessed requests in batches of 20, assigns each to one of 40 pre-funded signing accounts, and processes them in parallel via a TypeScript subprocess.

**Strengths**:
- Asynchronous queue-driven processing (requests are never dropped)
- 40 signing accounts across 2 batches for parallel throughput
- Database persistence with full audit trail (tx hash, errors, timestamps)
- Atomic database locking prevents duplicate processing
- Laravel Horizon for real-time queue monitoring
- Rate limiting: MAV one-time ever, MVN/USDT daily per address

**Weaknesses**:
- Telegram-only input — no web UI, no wallet connect, no dApp experience
- 40 private keys to generate, fund, monitor, and rotate — heavy operational burden
- Hardcoded token amounts (not configurable)
- Python dependency (Telethon) adds operational complexity
- No PoW/CAPTCHA — rate limiting is address-based only
- Tightly coupled to Telegram as the sole request channel

---

## Comparison Summary

| Aspect | Implementation A (React + Express) | Implementation B (Laravel + Telegram) |
| --- | --- | --- |
| User experience | Web dApp with wallet connect | Post address in Telegram |
| Request handling | Synchronous, blocking | Async queue, batch jobs |
| Signing accounts | 1 | 40 |
| Throughput | ~1 tx/block, fails under load | ~40 tx/block |
| Anti-abuse | PoW + CAPTCHA | Address-based rate limits |
| Persistence | None (Redis ephemeral) | Full DB audit trail |
| Monitoring | None | Laravel Horizon |
| Operational complexity | Low | High (40 keys, Python bot) |

---

## Proposed Architecture

### Core Idea

Keep the current frontend (React dApp with wallet connect and PoW/CAPTCHA), but replace the backend’s synchronous single-transaction model with a **queue + batch** system using a **single signing account**.

Instead of sending one transaction per request and blocking the HTTP response, the backend accepts requests into a queue immediately and a scheduled worker drains the queue every block cycle, batching all pending transfers into a single on-chain operation.

### Request Flow

```
1. User connects wallet on web UI
2. Completes CAPTCHA and/or PoW challenges
3. Submits faucet request

4. Backend validates the request (PoW solution, eligibility checks)
5. Stores the request in a persistent queue (database)
6. Responds immediately with a request ID

7. Worker runs every ~15 seconds (one block cycle):
   a. Drains the queue (up to MAX_BATCH_SIZE requests)
   b. Builds a single batched transaction:
      contract.batch([transfer1, transfer2, ...])
   c. Submits one transaction to the chain
   d. Waits for confirmation
   e. Updates all requests in the batch with the tx hash

8. User polls for status using their request ID
   or receives a notification when their tokens arrive
```

### Why One Address Is Enough

The 40-account approach in Implementation B exists to avoid nonce conflicts — a single account can only have one pending transaction per counter value. Concurrent sends from the same signer collide.

Batching solves this differently: all transfers are grouped into one operation with one counter increment. No concurrency, no conflicts, one key to manage.

**Throughput comparison**:
- Implementation A: ~1 tx/block (sequential, blocking)
- Implementation B: ~40 tx/block (40 parallel accounts)
- Proposed: **~50-100 transfers/block** (one batched tx) — actually higher, with one key

A simple mv transfer costs ~1,400 gas. FA2 token transfers cost more but are still lightweight. The per-block gas limit comfortably fits 50-100 simple transfers in a single batch. If the queue has more than MAX_BATCH_SIZE, the remainder carries over to the next cycle.

### Key Parameters

| Parameter | Value | Rationale |
| --- | --- | --- |
| Poll interval | ~15 seconds | Match block time |
| MAX_BATCH_SIZE | 50-100 | Gas limit per block (needs testing) |
| Request TTL | 30 minutes | Auto-expire unclaimed requests |
| Confirmation polling | 2 seconds | Standard Taquito confirmation |

### What Changes

**Frontend**: Minimal changes. After submitting a request, instead of waiting for a direct response with a tx hash, the user gets a request ID and sees a “pending” state. The UI polls or subscribes for the result. The PoW/CAPTCHA flow stays the same.

**Backend**:
- New persistent storage layer (database table for faucet requests)
- New batch worker (cron or setInterval-based, runs every block cycle)
- `/verify` endpoint becomes non-blocking: validates and enqueues, responds immediately
- New `/status/:requestId` endpoint for polling
- Batch transaction logic using `contract.batch()`
- Single signing account (no change from current)

**What stays the same**:

- React frontend, wallet connect, PoW challenges, CAPTCHA
- Configuration format (config.json)
- Single faucet private key

---

## Proposed New Features

### 1. Mainnet MVRK Balance Gate

**Requirement**: A user must hold at least 10 MVRK on mainnet to be eligible for testnet faucet tokens.

**Rationale**: This prevents throwaway accounts from draining the faucet. Anyone serious enough to test on a testnet likely holds some MVRK on mainnet. This is a stronger anti-sybil measure than PoW or CAPTCHA alone.

**Implementation**:

- After wallet connect, the backend queries the user's mainnet balance via the mainnet RPC endpoint.
- If the balance is below the threshold (e.g. 10 MVRK), the request is rejected with a clear message.
- The threshold should be configurable via `config.json` or environment variable.
- The mainnet RPC URL should also be configurable (separate from the testnet RPC used for faucet operations).

**Considerations**:

- The check should happen early (before PoW challenges) to avoid wasting the user's time.
- Mainnet RPC calls should be cached briefly (e.g. 60 seconds per address) to avoid redundant lookups if the user retries.
- The threshold should be low enough to not exclude legitimate developers but high enough to deter bots. 10 MVRK is a reasonable starting point.

**Config example**:

```json
{
  "application": {
    "mainnetBalanceCheck": {
      "enabled": true,
      "rpcUrl": "https://mainnet.rpc.mavryk.network",
      "minBalance": 10
    }
  }
}
```

### 2. 24-Hour Cooldown Per Address

**Requirement**: Each address can only receive faucet tokens once every 24 hours, regardless of token type.

**Rationale**: The current PoW system limits abuse per session but does not prevent the same address from requesting repeatedly across sessions. A hard 24-hour cooldown ensures fair distribution.

**Implementation**:

- The faucet request database table records every successful distribution with a timestamp.
- Before processing a new request, the backend checks if the address has received any tokens in the last 24 hours.
- If yes, the request is rejected with a message indicating the remaining cooldown time.
- This check should happen at request submission time (not at batch processing time) so the user gets immediate feedback.

**Considerations**:

- The cooldown could be per-token-type (like Tim's faucet: MAV one-time, others daily) or global (one request per 24h for any token). A global cooldown is simpler and stricter.
- The cooldown window and scope (per-token vs global) should be configurable.
- With the proposed database-backed request storage, this check is a simple query and adds negligible overhead.
- Edge case: if a batched transaction fails on-chain, the failed requests should not count toward the cooldown. Only successful distributions (with a tx hash) should block future requests.

**Config example**:

```json
{
  "application": {
    "cooldown": {
      "enabled": true,
      "hours": 24,
      "scope": "global"
    }
  }
}
```

---

## Summary

|  | Current (A) | Tim's (B) | Proposed |
| --- | --- | --- | --- |
| Frontend | React dApp | Telegram | React dApp |
| Request model | Synchronous | Async queue | Async queue |
| Signing accounts | 1 | 40 | 1 |
| Batch transactions | No | No (1 tx per account) | Yes |
| Throughput/block | ~1 | ~40 | ~50-100 |
| Anti-abuse | PoW + CAPTCHA | Address rate limits | PoW + CAPTCHA + mainnet balance gate + 24h cooldown |
| Persistence | None | Full DB | Full DB |
| Operational burden | Low | High | Low |
| Monitoring | None | Horizon | TBD (can add) |

The proposed design takes the user-facing strengths of Implementation A (web dApp, wallet connect, PoW) and the backend strengths of Implementation B (queue, persistence, async processing), while eliminating the weaknesses of both (blocking requests, 40 keys to manage). The batch-per-block model with a single address is simpler to operate and delivers higher throughput than either current implementation.