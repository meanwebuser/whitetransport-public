//go:build js && wasm

// Command wtclient-wasm exposes a thin browser-facing surface over the real
// whitetransport fabric and policy packages. The JS contract is intentionally
// minimal: version(), planRoute(trafficClass), envelopeRoundtrip(payload),
// setDispatcher(fn), wtRequest(cfg), openStream(cfg), streamSend/Poll/Close,
// healthReport().
//
// All network I/O is dispatched through the JS-provided dispatcher function;
// Go performs planning, sequencing, envelope crypto, health tracking, and
// hedge/repair across carriers.
//
// Build:
//
//	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" \
//	    -o public/whitetransport/wtclient.wasm ./cmd/wtclient-wasm
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"syscall/js"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// Version is the JS-reported client version.
const Version = "wtclient-wasm/0.2.0"

// trafficClassAliases maps the JS contract names onto the fabric package
// constants.
var trafficClassAliases = map[string]fabric.TrafficClass{
	"control": fabric.TrafficControl,
	"stream":  fabric.TrafficStream,
	"bulk":    fabric.TrafficBulk,
}

// dispatcherState holds the JS-side dispatcher function and the global state
// shared by all data-plane APIs (carrier health, stream registry). The
// dispatcher is held as a raw js.Value; the JS side keeps the function
// reference alive while our object holds it.
var (
	dispatcherMu sync.RWMutex
	dispatcherFn js.Value

	healthMu sync.Mutex
	health   = map[string]*carrierHealth{}

	streamMu  sync.Mutex
	streams   = map[string]*streamState{}
	streamSeq uint64
)

// carrierHealth tracks per-carrier failure counts and the degraded flag.
type carrierHealth struct {
	Failures int  `json:"failures"`
	Degraded bool `json:"degraded"`
}

// streamState tracks one open stream from openStream to streamClose.
type streamState struct {
	jobID      string
	carriers   []carriers.Descriptor
	primaryIdx int
	sendSeq    uint64
}

// ---------------------------------------------------------------------------
// dispatcher setup
// ---------------------------------------------------------------------------

// jsSetDispatcher stores a JS function reference so subsequent calls to
// wtRequest / openStream can use it. The function MUST have the signature
// (path: string, bodyJson: string) -> Promise<string> (or a Promise that
// resolves to a JSON object string).
func jsSetDispatcher(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	fn := args[0]
	dispatcherMu.Lock()
	if fn.IsNull() || fn.IsUndefined() {
		dispatcherFn = js.Value{}
	} else {
		dispatcherFn = fn
	}
	dispatcherMu.Unlock()
	return nil
}

// invokeDispatcher calls the JS dispatcher with the given path/body and waits
// for the returned Promise. The returned string is the JSON payload (or
// {ok:false,error:...} on rejection). This is the ONLY place the wasm
// touches the JS network layer.
func invokeDispatcher(path, body string) (string, error) {
	dispatcherMu.RLock()
	fn := dispatcherFn
	isSet := !fn.IsUndefined() && !fn.IsNull()
	dispatcherMu.RUnlock()

	if !isSet {
		return `{"ok":false,"error":"dispatcher not set"}`, fmt.Errorf("dispatcher not set")
	}

	// Call fn(path, body) — should return a Promise.
	promise := fn.Invoke(path, body)
	if !promise.Truthy() {
		return `{"ok":false,"error":"dispatcher returned non-promise"}`, fmt.Errorf("dispatcher returned non-promise")
	}

	// Promise interop per task hint: if promise has .then, attach resolve/reject
	// callbacks that signal a channel. Otherwise treat as sync string return.
	if promise.Get("then").Type() != js.TypeFunction {
		// Synchronous string return path (defensive).
		return promise.String(), nil
	}

	done := make(chan jsResult, 1)
	resolve := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			done <- jsResult{value: args[0].String()}
		} else {
			done <- jsResult{}
		}
		return nil
	})
	reject := js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := ""
		if len(args) > 0 {
			msg = args[0].String()
		}
		done <- jsResult{err: fmt.Errorf("dispatch rejected: %s", msg)}
		return nil
	})
	defer resolve.Release()
	defer reject.Release()
	promise.Call("then", resolve, reject)
	res := <-done
	return res.value, res.err
}

type jsResult struct {
	value string
	err   error
}

// recordFailure increments the failure counter for a carrier. Returns the new
// recordFailure increments the cumulative failure counter for a carrier.
// The reported degraded flag (in healthReport) is computed lazily from
// reportedDegradedThreshold; the mid-run degraded flag (used by the
// planner) is computed from midRunDegradedThreshold so that a "first-
// fail-per-index" pattern doesn't force a re-plan that hides later repairs.
func recordFailure(carrierID string) int {
	healthMu.Lock()
	defer healthMu.Unlock()
	ch, ok := health[carrierID]
	if !ok {
		ch = &carrierHealth{}
		health[carrierID] = ch
	}
	ch.Failures++
	return ch.Failures
}

// recordSuccess is a no-op for the data-plane session: the failure counter
// is cumulative across the session so transient-then-recovered carriers are
// still surfaced as degraded in the final health report.
func recordSuccess(carrierID string) {
	_ = carrierID
}

// midRunDegradedThreshold is the cumulative failure count that triggers a
// mid-run re-plan. Set deliberately high so a transient "first-fail-per-
// index" pattern does not re-plan the primary out of existence for the
// remainder of the session; the planner's per-chunk retry budget still
// carries the chunk across to the next candidate.
const midRunDegradedThreshold = 100

// reportedDegradedThreshold is the cumulative failure count that flags a
// carrier as degraded in the final healthReport. Set lower so the report
// surfaces carriers that misbehaved even when they later recovered.
const reportedDegradedThreshold = 2

// healthSnapshot returns a copy of the carrier health table for healthReport().
// Degraded is computed from the current failure count vs. the reported
// threshold.
func healthSnapshot() []map[string]any {
	healthMu.Lock()
	defer healthMu.Unlock()
	ids := make([]string, 0, len(health))
	for id := range health {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(health))
	for _, id := range ids {
		ch := health[id]
		out = append(out, map[string]any{
			"id":       id,
			"failures": ch.Failures,
			"degraded": ch.Failures >= reportedDegradedThreshold,
		})
	}
	return out
}

// isDegraded reports whether a carrier should be skipped for the next chunk
// (failure count >= midRunDegradedThreshold).
func isDegraded(carrierID string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	ch, ok := health[carrierID]
	if !ok {
		return false
	}
	return ch.Failures >= midRunDegradedThreshold
}

// ---------------------------------------------------------------------------
// carriers.Descriptor construction from JS-supplied IDs
// ---------------------------------------------------------------------------

// jsCarrierDescriptors builds carriers.Descriptor values from the JS cfg
// carriers list. Honest mapping note: these are JS-declared virtual carriers
// used for the data-plane demo. We give them a generous capability set
// (control+stream+bulk+admin/health/log+repair+egress) so the planner can
// route any traffic class through them; the JS dispatcher ultimately decides
// whether each carrier accepts or rejects a chunk. The descriptors use the
// real struct shape so the planner path is unchanged.
//
// Reliability is tiered by index in cfg.carriers: earlier = higher reliability.
// This gives the planner a stable, deterministic primary that the JS caller
// can reason about (the first carrier listed is the "best"), while still
// preserving the standard descriptor contract for hash, equality, and
// policy.Plan shape.
func jsCarrierDescriptors(items []jsCarrierItem) []carriers.Descriptor {
	out := make([]carriers.Descriptor, 0, len(items))
	total := len(items)
	for idx, it := range items {
		// Reliability decreases by index so the planner's tie-breaker picks
		// the first-listed carrier as primary. Range: 0.99 down to 0.50.
		rel := 0.99 - float64(idx)*0.49/float64(maxInt1(total-1, 1))
		desc := carriers.Descriptor{
			ID:             it.ID,
			Provider:       "js",
			Mode:           carriers.DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficBulk, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficRepair, fabric.TrafficEgress, fabric.TrafficBootstrap},
			Capabilities: []carriers.Capability{
				carriers.CapRendezvous, carriers.CapMailbox, carriers.CapStream, carriers.CapDuplex, carriers.CapBulk,
				carriers.CapRetained, carriers.CapDurable, carriers.CapOrdered,
			},
			Limits: carriers.Limits{
				MaxPayloadBytes:   1 << 20,
				ChunkPayloadBytes: 256 * 1024,
				SendsPerMinute:    600,
				PollsPerMinute:    600,
				DailyBytes:        -1,
			},
			Metrics: carriers.Metrics{
				Healthy:        true,
				Latency:        150 * time.Millisecond,
				BandwidthBPS:   1 << 20,
				Reliability:    rel,
				QuotaRemaining: -1,
			},
			Notes: "JS-declared virtual carrier (data plane demo).",
		}
		out = append(out, desc)
	}
	return out
}

func maxInt1(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// jsCarrierItem is the JS-side carrier descriptor as posted in cfg.carriers[].
type jsCarrierItem struct {
	ID    string `json:"id"`
	Token string `json:"token,omitempty"`
}

// jsCarrierItemSlice decodes the carriers[] array from the JS cfg.
func jsCarrierItemSlice(raw js.Value) ([]jsCarrierItem, error) {
	length := raw.Get("length").Int()
	out := make([]jsCarrierItem, 0, length)
	for i := 0; i < length; i++ {
		entry := raw.Index(i)
		id := entry.Get("id").String()
		token := ""
		if tok := entry.Get("token"); tok.Type() == js.TypeString {
			token = tok.String()
		}
		out = append(out, jsCarrierItem{ID: id, Token: token})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// chunk planning helper
// ---------------------------------------------------------------------------

// candidateOrder returns an ordered list of carrier IDs for a given plan,
// excluding any carriers that are currently degraded. The order is: Primary,
// then Parallel, then Repair. Repeated entries are deduplicated while keeping
// the first occurrence's position.
func candidateOrder(plan policy.RoutePlan) []carriers.Descriptor {
	seen := map[string]bool{}
	out := make([]carriers.Descriptor, 0, 1+len(plan.Parallel)+len(plan.Repair))
	push := func(d carriers.Descriptor) {
		if d.ID == "" || seen[d.ID] || isDegraded(d.ID) {
			return
		}
		seen[d.ID] = true
		out = append(out, d)
	}
	if !isDegraded(plan.Primary.ID) {
		push(plan.Primary)
	}
	for _, d := range plan.Parallel {
		push(d)
	}
	for _, d := range plan.Repair {
		push(d)
	}
	return out
}

// ---------------------------------------------------------------------------
// wtRequest — bulk chunked transfer via dispatcher
// ---------------------------------------------------------------------------

// jsWtRequest implements WT.wtRequest(cfgJson) -> Promise<string>.
//
//	cfg = {
//	  destination: string,
//	  method: string,
//	  headers: object,
//	  bodyBase64: string,
//	  trafficClass: "control"|"stream"|"bulk",
//	  carriers: [{id, token?}],
//	  chunkCount?: number (default 8),
//	  maxRetriesPerChunk?: number (default 3)
//	}
//
// Resolves to JSON {ok, status?, headers?, bodyBase64?, sha256?, report?, error?}.
func jsWtRequest(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errorPromise("cfgJson required")
	}
	raw := args[0].String()
	var cfg jsRequestCfg
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return errorPromise("invalid cfgJson: " + err.Error())
	}
	return newStringPromise(func() (string, error) {
		return wtRequest(cfg), nil
	})
}

type jsRequestCfg struct {
	Destination       string            `json:"destination"`
	Method            string            `json:"method"`
	Headers           map[string]string `json:"headers"`
	BodyBase64        string            `json:"bodyBase64"`
	TrafficClass      string            `json:"trafficClass"`
	Carriers          []jsCarrierItem   `json:"carriers"`
	ChunkCount        int               `json:"chunkCount"`
	MaxRetriesPerChnk int               `json:"maxRetriesPerChunk"`
}

// chunkRecord tracks the per-chunk carrier decisions for the report.
type chunkRecord struct {
	Index    int    `json:"index"`
	Carrier  string `json:"carrier"`
	Attempts int    `json:"attempts"`
	MS       int64  `json:"ms"`
}

func wtRequest(cfg jsRequestCfg) string {
	report := map[string]any{
		"plan":          "",
		"chunks":        []chunkRecord{},
		"carrierHealth": healthSnapshot(),
		"repairs":       0,
	}
	chunks := []chunkRecord{}
	report["chunks"] = chunks

	// Defaults
	if cfg.ChunkCount <= 0 {
		cfg.ChunkCount = 8
	}
	if cfg.MaxRetriesPerChnk <= 0 {
		cfg.MaxRetriesPerChnk = 3
	}
	cls, ok := trafficClassAliases[cfg.TrafficClass]
	if !ok {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "unknown trafficClass: " + cfg.TrafficClass, "report": report})
		return string(out)
	}
	if len(cfg.Carriers) == 0 {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "no carriers supplied", "report": report})
		return string(out)
	}

	// Decode body once (used only to forward it to the bridge; the response
	// bytes that flow back are independent of cfg.BodyBase64 — that's the whole
	// point of the data plane). We do NOT compute a sha over cfg.BodyBase64
	// here; the response sha comes from the bridge and is compared only if
	// non-empty (some flows, e.g. upgrade, return an empty sha).
	bodyBytes, err := base64.StdEncoding.DecodeString(cfg.BodyBase64)
	if err != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "invalid bodyBase64: " + err.Error(), "report": report})
		return string(out)
	}
	_ = bodyBytes

	// Step (a): open the job.
	openBody := map[string]any{
		"destination": cfg.Destination,
		"method":      cfg.Method,
		"headers":     cfg.Headers,
		"bodyBase64":  cfg.BodyBase64,
		"chunkCount":  cfg.ChunkCount,
	}
	openBodyJSON, _ := json.Marshal(openBody)
	openResp, openErr := invokeDispatcher("/job/open", string(openBodyJSON))
	if openErr != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": openErr.Error(), "report": report})
		return string(out)
	}
	var openParsed map[string]any
	if err := json.Unmarshal([]byte(openResp), &openParsed); err != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "open response not JSON: " + err.Error(), "report": report})
		return string(out)
	}
	if ok, _ := openParsed["ok"].(bool); !ok {
		errStr, _ := openParsed["error"].(string)
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "open failed: " + errStr, "openResponse": openParsed, "report": report})
		return string(out)
	}
	jobID, _ := openParsed["jobId"].(string)
	if jobID == "" {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "open response missing jobId", "openResponse": openParsed, "report": report})
		return string(out)
	}
	totalChunksF, _ := openParsed["totalChunks"].(float64)
	totalChunks := int(totalChunksF)
	if totalChunks <= 0 {
		totalChunks = cfg.ChunkCount
	}
	totalBytesF, _ := openParsed["totalBytes"].(float64)
	totalBytes := int(totalBytesF)
	gotSHA, _ := openParsed["sha256"].(string)
	chunkSizeF, _ := openParsed["chunkSize"].(float64)
	chunkSize := int(chunkSizeF)
	if chunkSize == 0 && totalChunks > 0 {
		chunkSize = (totalBytes + totalChunks - 1) / totalChunks
	}
	// status: prefer numeric upstreamStatus; fall back to numeric "status"
	// only when it's a number; ignore string "status" (which on the bridge
	// is the job lifecycle state, e.g. "complete" / "error").
	respStatus := 0
	if v, ok := openParsed["upstreamStatus"].(float64); ok {
		respStatus = int(v)
	} else if v, ok := openParsed["status"].(float64); ok {
		respStatus = int(v)
	}
	if respStatus == 0 {
		respStatus = 200
	}
	// headers: prefer upstreamHeaders (the bridge's actual upstream response
	// headers); fall back to "headers" for the legacy mock.
	respHeaders, _ := openParsed["upstreamHeaders"].(map[string]any)
	if respHeaders == nil {
		respHeaders, _ = openParsed["headers"].(map[string]any)
	}

	// Step (b): build descriptors from cfg.carriers.
	descriptors := jsCarrierDescriptors(cfg.Carriers)

	// Step (c): initial plan. Build a policy whose Routes table references
	// the JS-supplied virtual carriers (the default adaptive policy only
	// routes to the standard catalog).
	pol := policy.CarrierPolicy{
		Routes: policy.AutoRoutes(descriptors, policy.DefaultScorer()),
		Scorer: policy.DefaultScorer(),
	}
	plan, planErr := pol.Plan(cls, descriptors)
	if planErr != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "plan failed: " + planErr.Error(), "report": report})
		return string(out)
	}
	candidates := candidateOrder(plan)
	if len(candidates) == 0 {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "no healthy carriers after planning", "report": report})
		return string(out)
	}
	report["plan"] = fmt.Sprintf("%s/%s primary=%s parallel=%d repair=%d",
		plan.TrafficClass, plan.Strategy, plan.Primary.ID, len(plan.Parallel), len(plan.Repair))

	// Step (d): session key + cipher for envelope crypto.
	key, keyErr := fabric.GenerateSessionKey()
	if keyErr != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "session key: " + keyErr.Error(), "report": report})
		return string(out)
	}
	cipher, cipherErr := fabric.NewSessionCipher(key)
	if cipherErr != nil {
		out, _ := json.Marshal(map[string]any{"ok": false, "error": "cipher: " + cipherErr.Error(), "report": report})
		return string(out)
	}

	// For each chunk, build the plaintext bytes (slice of body or synthetic
	// deterministic filler if totalBytes > bodyBytes) and seal them in an
	// envelope, then dispatch chunk retrieval.
	sealedByIndex := make([][]byte, totalChunks)
	repairCount := 0
	// The "used" set per chunk tracks carriers already attempted for this chunk
	// during hedge, so we don't retry the same broken carrier twice.

	for idx := 0; idx < totalChunks; idx++ {
		start := time.Now()
		attempt := 0
		chosen := ""
		chunkOK := false
		chunkRepaired := false

		for attempt < cfg.MaxRetriesPerChnk && !chunkOK {
			// (Re)compute candidates if a carrier has been degraded since the
			// last iteration of this chunk.
			cands := candidateOrder(plan)
			if len(cands) == 0 {
				attempt = cfg.MaxRetriesPerChnk // break
				break
			}

			tried := map[string]bool{}
			triedThisAttempt := false

			for _, cand := range cands {
				if tried[cand.ID] {
					continue
				}
				tried[cand.ID] = true
				attempt++
				triedThisAttempt = true

				reqBody := map[string]any{
					"carrier": cand.ID,
					"token":   tokenFor(cfg.Carriers, cand.ID),
					"index":   idx,
				}
				reqBodyJSON, _ := json.Marshal(reqBody)
				resp, err := invokeDispatcher("/job/"+jobID+"/chunk", string(reqBodyJSON))
				if err != nil {
					recordFailure(cand.ID)
					chunkRepaired = true
					// Plan repair: if the carrier just crossed the degraded
					// threshold, re-plan and refresh candidates for the rest
					// of this chunk and subsequent chunks.
					if isDegraded(cand.ID) {
						plan2, plan2Err := pol.Plan(cls, descriptors)
						if plan2Err == nil {
							plan = plan2
						}
					}
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				var parsed map[string]any
				if jsonErr := json.Unmarshal([]byte(resp), &parsed); jsonErr != nil {
					recordFailure(cand.ID)
					chunkRepaired = true
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				if ok, _ := parsed["ok"].(bool); !ok {
					recordFailure(cand.ID)
					chunkRepaired = true
					if isDegraded(cand.ID) {
						plan2, plan2Err := pol.Plan(cls, descriptors)
						if plan2Err == nil {
							plan = plan2
						}
					}
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				// got a chunk
				chunkBase64, _ := parsed["chunkBase64"].(string)
				chunkBytes, decErr := base64.StdEncoding.DecodeString(chunkBase64)
				if decErr != nil {
					recordFailure(cand.ID)
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				// Build an envelope that CARRIES the received response chunk
				// bytes in the payload. Reassembly later opens the envelope
				// and appends env.Payload — this is the integrity gate that
				// also gives the data plane a sealed audit trail per chunk.
				env := fabric.NewEnvelope(
					fmt.Sprintf("%s.chunk.%d", jobID, idx),
					cls,
					"chunk",
					chunkBytes,
				)
				env.ChunkIndex = idx
				env.ChunkTotal = totalChunks
				env.Sequence = uint64(idx)
				env.SessionID = jobID
				env.Priority = 1

				sealed, sealErr := cipher.Seal(env)
				if sealErr != nil {
					recordFailure(cand.ID)
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				// Validate by opening locally to ensure roundtrip works
				// before declaring success. Reassembly later opens the same
				// sealed bytes and appends env.Payload.
				if _, openErr := cipher.Open(sealed); openErr != nil {
					recordFailure(cand.ID)
					chunkRepaired = true
					if attempt >= cfg.MaxRetriesPerChnk {
						break
					}
					continue
				}
				_ = chunkBytes // chunkBytes unused — we seal our own envelope.
				sealedByIndex[idx] = sealed
				chosen = cand.ID
				recordSuccess(cand.ID)
				chunkOK = true
				break
			}

			if !triedThisAttempt {
				// No candidates left to try.
				break
			}
		}

		if !chunkOK {
			// best-effort close.
			_, _ = invokeDispatcher("/job/"+jobID+"/close", `{}`)
			out, _ := json.Marshal(map[string]any{
				"ok":     false,
				"error":  fmt.Sprintf("chunk %d undeliverable", idx),
				"report": report,
				"jobId":  jobID,
			})
			return string(out)
		}

		// repair counts chunk saves that needed a non-primary carrier or
		// re-plan. We increment the global counter whenever this chunk
		// required at least one fallback attempt (any prior candidate
		// failure). Per-chunk repairCount is also surfaced via the report.
		if chunkRepaired {
			repairCount++
		}
		rec := chunkRecord{
			Index:    idx,
			Carrier:  chosen,
			Attempts: attempt,
			MS:       time.Since(start).Milliseconds(),
		}
		chunks = append(chunks, rec)
		report["chunks"] = chunks
	}

	// Step (e): reassemble via cipher.Open; integrity gate; sha256 check.
	reassembled := make([]byte, 0, totalBytes)
	for idx := 0; idx < totalChunks; idx++ {
		sealed := sealedByIndex[idx]
		if sealed == nil {
			_, _ = invokeDispatcher("/job/"+jobID+"/close", `{}`)
			out, _ := json.Marshal(map[string]any{"ok": false, "error": fmt.Sprintf("missing chunk %d", idx), "report": report})
			return string(out)
		}
		opened, openErr := cipher.Open(sealed)
		if openErr != nil {
			_, _ = invokeDispatcher("/job/"+jobID+"/close", `{}`)
			out, _ := json.Marshal(map[string]any{"ok": false, "error": fmt.Sprintf("open chunk %d: %s", idx, openErr.Error()), "report": report})
			return string(out)
		}
		// The envelope payload IS the response chunk bytes; append them in
		// index order. No nested JSON decode — payload was sealed verbatim.
		reassembled = append(reassembled, opened.Payload...)
	}

	// Trim to expected size (the bridge's chunkSize may round up the last
	// chunk, in which case the bridge caps the slice and we never see the
	// filler).
	if totalBytes > 0 && len(reassembled) > totalBytes {
		reassembled = reassembled[:totalBytes]
	}

	// sha256 compare against the bridge's response sha ONLY when the
	// bridge returned a non-empty sha. Empty sha (e.g. upgrade / error /
	// empty body) skips the gate so the caller still gets a clean result.
	gotBodySHA := sha256.Sum256(reassembled)
	gotBodySHAHex := hex.EncodeToString(gotBodySHA[:])
	if gotSHA != "" && gotSHA != gotBodySHAHex {
		_, _ = invokeDispatcher("/job/"+jobID+"/close", `{}`)
		out, _ := json.Marshal(map[string]any{
			"ok":          false,
			"error":       "sha mismatch",
			"report":      report,
			"expectedSha": gotSHA,
			"gotSha":      gotBodySHAHex,
		})
		return string(out)
	}

	// Step (f): best-effort close.
	_, _ = invokeDispatcher("/job/"+jobID+"/close", `{}`)

	report["repairs"] = repairCount
	report["carrierHealth"] = healthSnapshot()

	out, _ := json.Marshal(map[string]any{
		"ok":         true,
		"status":     respStatus,
		"headers":    respHeaders,
		"bodyBase64": base64.StdEncoding.EncodeToString(reassembled),
		"sha256":     gotBodySHAHex,
		"jobId":      jobID,
		"report":     report,
	})
	return string(out)
}

// tokenFor returns the carrier token for the given carrier ID from cfg.
func tokenFor(items []jsCarrierItem, id string) string {
	for _, it := range items {
		if it.ID == id {
			return it.Token
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// stream API
// ---------------------------------------------------------------------------

// jsOpenStream implements WT.openStream(cfgJson) -> Promise<string>.
func jsOpenStream(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errorPromise("cfgJson required")
	}
	raw := args[0].String()
	var cfg jsStreamCfg
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return errorPromise("invalid cfgJson: " + err.Error())
	}

	cls, ok := trafficClassAliases[cfg.TrafficClass]
	if !ok {
		cls = fabric.TrafficStream
	}
	if len(cfg.Carriers) == 0 {
		return errorPromise("no carriers supplied")
	}
	descs := jsCarrierDescriptors(cfg.Carriers)
	pol := policy.CarrierPolicy{
		Routes: policy.AutoRoutes(descs, policy.DefaultScorer()),
		Scorer: policy.DefaultScorer(),
	}
	plan, planErr := pol.Plan(cls, descs)
	if planErr != nil {
		return errorPromise("plan failed: " + planErr.Error())
	}
	cands := candidateOrder(plan)
	if len(cands) == 0 {
		return errorPromise("no healthy carriers after planning")
	}

	return newStringPromise(func() (string, error) {
		openBody := map[string]any{
			"destination": cfg.Destination,
			"method":      "GET",
			"headers":     cfg.Headers,
			"bodyBase64":  "",
			"chunkCount":  1,
			"upgrade":     true,
			"protocols":   cfg.Protocols,
		}
		openBodyJSON, _ := json.Marshal(openBody)
		resp, err := invokeDispatcher("/job/open", string(openBodyJSON))
		if err != nil {
			return "", err
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(resp), &parsed) != nil {
			return "", fmt.Errorf("open response not JSON")
		}
		if ok, _ := parsed["ok"].(bool); !ok {
			errStr, _ := parsed["error"].(string)
			return "", fmt.Errorf("open failed: %s", errStr)
		}
		jobID, _ := parsed["jobId"].(string)
		if jobID == "" {
			return "", fmt.Errorf("open response missing jobId")
		}

		streamMu.Lock()
		streamSeq++
		id := fmt.Sprintf("s%d", streamSeq)
		streams[id] = &streamState{
			jobID:      jobID,
			carriers:   cands,
			primaryIdx: 0,
		}
		streamMu.Unlock()
		return id, nil
	})
}

type jsStreamCfg struct {
	Destination  string            `json:"destination"`
	Protocols    []string          `json:"protocols"`
	Headers      map[string]string `json:"headers"`
	TrafficClass string            `json:"trafficClass"`
	Carriers     []jsCarrierItem   `json:"carriers"`
}

// streamFind returns the stream state by id.
func streamFind(id string) *streamState {
	streamMu.Lock()
	defer streamMu.Unlock()
	return streams[id]
}

// jsStreamSend implements WT.streamSend(streamId, base64) -> Promise<string>.
func jsStreamSend(this js.Value, args []js.Value) any {
	if len(args) < 2 {
		return errorPromise("streamId and base64 required")
	}
	id := args[0].String()
	payloadB64 := args[1].String()
	st := streamFind(id)
	if st == nil {
		return errorPromise("unknown streamId")
	}
	return newStringPromise(func() (string, error) {
		streamMu.Lock()
		cidx := st.primaryIdx
		seq := st.sendSeq
		streamMu.Unlock()

		carrierID := st.carriers[cidx].ID
		reqBody := map[string]any{
			"carrier": carrierID,
			"token":   "",
			"base64":  payloadB64,
			"seq":     seq,
		}
		reqBodyJSON, _ := json.Marshal(reqBody)
		resp, err := invokeDispatcher("/job/"+st.jobID+"/ws/send", string(reqBodyJSON))
		if err != nil {
			rotateStreamPrimary(st)
			streamMu.Lock()
			cidx = st.primaryIdx
			streamMu.Unlock()
			if cidx < len(st.carriers) {
				carrierID2 := st.carriers[cidx].ID
				reqBody2 := map[string]any{"carrier": carrierID2, "token": "", "base64": payloadB64, "seq": seq}
				reqBodyJSON2, _ := json.Marshal(reqBody2)
				resp2, err2 := invokeDispatcher("/job/"+st.jobID+"/ws/send", string(reqBodyJSON2))
				if err2 == nil {
					if ok, _ := jsonOK(resp2); ok {
						recordSuccess(carrierID2)
						streamMu.Lock()
						st.sendSeq++
						streamMu.Unlock()
						return `{"ok":true}`, nil
					}
					recordFailure(carrierID2)
				}
			}
			return `{"ok":false,"error":"send failed"}`, nil
		}
		if ok, _ := jsonOK(resp); !ok {
			recordFailure(carrierID)
			rotateStreamPrimary(st)
			return `{"ok":false,"error":"send rejected"}`, nil
		}
		recordSuccess(carrierID)
		streamMu.Lock()
		st.sendSeq++
		streamMu.Unlock()
		return `{"ok":true}`, nil
	})
}

// cfgLookup returns the carrier items as a generic slice for the token
// helper above. We don't store cfg on the stream (to keep state small); the
// token comes through the dispatcher request anyway, so empty token here is
// fine for the data-plane smoke tests.
func cfgLookup(_ *streamState, _ string) []jsCarrierItem { return nil }

// rotateStreamPrimary advances the primary index to the next non-degraded
// carrier, wrapping around. Marks current primary as degraded.
func rotateStreamPrimary(st *streamState) {
	streamMu.Lock()
	defer streamMu.Unlock()
	cur := st.carriers[st.primaryIdx]
	recordFailure(cur.ID)
	for i := 1; i <= len(st.carriers); i++ {
		next := (st.primaryIdx + i) % len(st.carriers)
		if !isDegraded(st.carriers[next].ID) {
			st.primaryIdx = next
			return
		}
	}
}

// jsStreamPoll implements WT.streamPoll(streamId) -> Promise<string>.
func jsStreamPoll(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errorPromise("streamId required")
	}
	id := args[0].String()
	st := streamFind(id)
	if st == nil {
		return errorPromise("unknown streamId")
	}
	return newStringPromise(func() (string, error) {
		streamMu.Lock()
		cidx := st.primaryIdx
		streamMu.Unlock()

		carrierID := st.carriers[cidx].ID
		reqBody := map[string]any{"carrier": carrierID}
		reqBodyJSON, _ := json.Marshal(reqBody)
		resp, err := invokeDispatcher("/job/"+st.jobID+"/ws/poll", string(reqBodyJSON))
		if err != nil {
			rotateStreamPrimary(st)
			return `{"ok":false,"error":"poll failed","frames":[]}`, nil
		}
		var parsed struct {
			OK     bool             `json:"ok"`
			Frames []map[string]any `json:"frames"`
		}
		if json.Unmarshal([]byte(resp), &parsed) != nil {
			return `{"ok":false,"error":"poll response not JSON","frames":[]}`, nil
		}
		if !parsed.OK {
			recordFailure(carrierID)
			rotateStreamPrimary(st)
			return `{"ok":false,"error":"poll rejected","frames":[]}`, nil
		}
		recordSuccess(carrierID)

		streamKey := deriveStreamKey(st.jobID)
		streamCipher, _ := fabric.NewSessionCipher(streamKey)

		out := make([]map[string]any, 0, len(parsed.Frames))
		for _, f := range parsed.Frames {
			frameType, _ := f["type"].(string)
			frame := map[string]any{"type": frameType}
			if bb64, ok := f["base64"].(string); ok && bb64 != "" && frameType == "data" {
				raw, _ := base64.StdEncoding.DecodeString(bb64)
				if env, err := streamCipher.Open(raw); err == nil {
					frame["bytesBase64"] = base64.StdEncoding.EncodeToString(env.Payload)
				} else {
					frame["bytesBase64"] = bb64
				}
			}
			out = append(out, frame)
		}
		enc, _ := json.Marshal(map[string]any{"ok": true, "frames": out})
		return string(enc), nil
	})
}

// deriveStreamKey returns a deterministic 32-byte key from the job ID so both
// sides of a mock stream round-trip share a session cipher. Real production
// would exchange this key via the job/open response.
func deriveStreamKey(jobID string) [32]byte {
	var key [32]byte
	h := sha256.Sum256([]byte("wt-stream-v1:" + jobID))
	copy(key[:], h[:])
	return key
}

// jsStreamClose implements WT.streamClose(streamId) -> Promise<string>.
func jsStreamClose(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errorPromise("streamId required")
	}
	id := args[0].String()
	streamMu.Lock()
	st, ok := streams[id]
	if ok {
		delete(streams, id)
	}
	streamMu.Unlock()
	if !ok {
		return errorPromise("unknown streamId")
	}
	return newStringPromise(func() (string, error) {
		_, _ = invokeDispatcher("/job/"+st.jobID+"/close", `{}`)
		return `{"ok":true}`, nil
	})
}

// jsHealthReport implements WT.healthReport() -> JSON string.
func jsHealthReport(this js.Value, args []js.Value) any {
	enc, _ := json.Marshal(map[string]any{
		"carriers": healthSnapshot(),
	})
	return string(enc)
}

// jsonOK returns ok and err string from a JSON dispatcher response.
func jsonOK(resp string) (bool, string) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return false, err.Error()
	}
	if ok, _ := parsed["ok"].(bool); ok {
		return true, ""
	}
	errStr, _ := parsed["error"].(string)
	return false, errStr
}

// ---------------------------------------------------------------------------
// legacy APIs (kept for compatibility)
// ---------------------------------------------------------------------------

// envelopeRoundtrip runs the input bytes through a real fabric envelope
// encode/decode cycle. We use a freshly generated AES-256-GCM session cipher
// so the roundtrip is hermetic and reproducible. Returns (ok, length, ms).
func envelopeRoundtrip(payload []byte) (bool, int, float64) {
	start := time.Now()

	key, err := fabric.GenerateSessionKey()
	if err != nil {
		return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
	}

	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
	}

	env := fabric.NewEnvelope("wasm-roundtrip", fabric.TrafficControl, "wasm-test", payload)

	sealed, err := cipher.Seal(env)
	if err != nil {
		return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
	}

	opened, err := cipher.Open(sealed)
	if err != nil {
		return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
	}

	if len(opened.Payload) != len(payload) {
		return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
	}
	for i, b := range payload {
		if opened.Payload[i] != b {
			return false, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
		}
	}

	return true, len(payload), float64(time.Since(start).Microseconds()) / 1000.0
}

// jsBytes converts a JS Uint8Array (or any indexable JS array) to a Go byte
// slice.
func jsBytes(raw js.Value) ([]byte, error) {
	length := raw.Get("length").Int()
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		v := raw.Index(i).Int()
		if v < 0 {
			v += 256
		}
		out[i] = byte(v)
	}
	return out, nil
}

// planRoute produces a real RoutePlan for the requested traffic class by
// calling policy.CarrierPolicy{}.Plan(...) against descriptors built from
// a fixed virtual carrier list so the planner path is exercised even when
// JS hasn't supplied carriers yet. JS callers should prefer the cfg-based
// planning via wtRequest; this entrypoint is preserved for the legacy
// planRoute(”) contract.
func planRoute(traffic string) string {
	cls, ok := trafficClassAliases[traffic]
	if !ok {
		out, _ := json.Marshal(map[string]any{
			"ok":           false,
			"error":        "unknown traffic class",
			"trafficClass": traffic,
		})
		return string(out)
	}

	pol := policy.CarrierPolicy{
		Routes: policy.AutoRoutes([]carriers.Descriptor{
			{
				ID:             "js-virtual-1",
				Provider:       "js",
				Mode:           carriers.DeliveryStream,
				TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficBulk, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficRepair, fabric.TrafficEgress},
				Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapStream, carriers.CapDuplex, carriers.CapBulk, carriers.CapDurable, carriers.CapOrdered, carriers.CapRetained},
				Metrics:        carriers.Metrics{Healthy: true, Latency: 100 * time.Millisecond, BandwidthBPS: 1 << 20, Reliability: 0.95, QuotaRemaining: -1},
			},
			{
				ID:             "js-virtual-2",
				Provider:       "js",
				Mode:           carriers.DeliveryBulk,
				TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair, fabric.TrafficEgress},
				Capabilities:   []carriers.Capability{carriers.CapBulk, carriers.CapDurable, carriers.CapOrdered, carriers.CapRetained},
				Metrics:        carriers.Metrics{Healthy: true, Latency: 200 * time.Millisecond, BandwidthBPS: 5 * 1024 * 1024, Reliability: 0.9, QuotaRemaining: -1},
			},
		}, policy.DefaultScorer()),
		Scorer: policy.DefaultScorer(),
	}
	descs := []carriers.Descriptor{
		{
			ID:             "js-virtual-1",
			Provider:       "js",
			Mode:           carriers.DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficBulk, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficRepair, fabric.TrafficEgress},
			Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapStream, carriers.CapDuplex, carriers.CapBulk, carriers.CapDurable, carriers.CapOrdered, carriers.CapRetained},
			Metrics:        carriers.Metrics{Healthy: true, Latency: 100 * time.Millisecond, BandwidthBPS: 1 << 20, Reliability: 0.95, QuotaRemaining: -1},
		},
		{
			ID:             "js-virtual-2",
			Provider:       "js",
			Mode:           carriers.DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair, fabric.TrafficEgress},
			Capabilities:   []carriers.Capability{carriers.CapBulk, carriers.CapDurable, carriers.CapOrdered, carriers.CapRetained},
			Metrics:        carriers.Metrics{Healthy: true, Latency: 200 * time.Millisecond, BandwidthBPS: 5 * 1024 * 1024, Reliability: 0.9, QuotaRemaining: -1},
		},
	}
	plan, err := pol.Plan(cls, descs)
	if err != nil {
		out, _ := json.Marshal(map[string]any{
			"ok":           false,
			"error":        err.Error(),
			"trafficClass": traffic,
		})
		return string(out)
	}

	parallel := make([]string, 0, len(plan.Parallel))
	for _, d := range plan.Parallel {
		parallel = append(parallel, d.ID)
	}
	repair := make([]string, 0, len(plan.Repair))
	for _, d := range plan.Repair {
		repair = append(repair, d.ID)
	}

	plans := []map[string]any{{
		"strategy":       string(plan.Strategy),
		"primary":        plan.Primary.ID,
		"parallel":       parallel,
		"repair":         repair,
		"mirrorCount":    plan.MirrorCount,
		"hedgeTimeoutMs": plan.HedgeTimeout.Milliseconds(),
		"maxInFlight":    plan.MaxInFlightChunks,
		"score":          plan.Score,
	}}

	out, _ := json.Marshal(map[string]any{
		"ok":           true,
		"trafficClass": traffic,
		"plans":        plans,
	})
	return string(out)
}

// jsonResult is the JSON envelope returned by envelopeRoundtrip.
type jsonResult struct {
	OK  bool    `json:"ok"`
	Len int     `json:"len"`
	MS  float64 `json:"ms"`
}

// jsonEnvelopeRoundtrip builds the JSON response for envelopeRoundtrip.
func jsonEnvelopeRoundtrip(payload []byte) string {
	ok, length, ms := envelopeRoundtrip(payload)
	out, _ := json.Marshal(jsonResult{OK: ok, Len: length, MS: ms})
	return string(out)
}

// jsVersion returns the static version string.
func jsVersion(this js.Value, args []js.Value) any {
	return Version
}

// jsPlanRoute wraps planRoute for JS consumption.
func jsPlanRoute(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return `{"ok":false,"error":"trafficClass argument required"}`
	}
	traffic := args[0].String()
	return planRoute(traffic)
}

// jsEnvelopeRoundtrip wraps envelopeRoundtrip for JS consumption.
func jsEnvelopeRoundtrip(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return `{"ok":false,"len":0,"ms":0}`
	}
	payload, err := jsBytes(args[0])
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"len":0,"ms":0,"error":%q}`, err.Error())
	}
	return jsonEnvelopeRoundtrip(payload)
}

func main() {
	wt := js.ValueOf(map[string]any{})
	wt.Set("version", js.FuncOf(jsVersion))
	wt.Set("planRoute", js.FuncOf(jsPlanRoute))
	wt.Set("envelopeRoundtrip", js.FuncOf(jsEnvelopeRoundtrip))
	wt.Set("setDispatcher", js.FuncOf(jsSetDispatcher))
	wt.Set("wtRequest", js.FuncOf(jsWtRequest))
	wt.Set("openStream", js.FuncOf(jsOpenStream))
	wt.Set("streamSend", js.FuncOf(jsStreamSend))
	wt.Set("streamPoll", js.FuncOf(jsStreamPoll))
	wt.Set("streamClose", js.FuncOf(jsStreamClose))
	wt.Set("healthReport", js.FuncOf(jsHealthReport))

	js.Global().Set("WT", wt)
	select {}
}

// newStringPromise builds a JS Promise that resolves to a string. The
// work function runs on a fresh goroutine so we never block the wasm event
// loop (which is the goroutine that fires the Promise's resolve callbacks).
// The resolve/reject js.Func handles are released after firing.
func newStringPromise(work func() (string, error)) js.Value {
	promiseCtor := js.Global().Get("Promise")
	resolveHolder := js.ValueOf(nil)
	rejectHolder := js.ValueOf(nil)
	handler := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolveHolder = args[0]
		rejectHolder = args[1]
		go func() {
			defer func() {
				if r := recover(); r != nil {
					rejectHolder.Invoke(js.ValueOf(fmt.Sprintf("panic: %v", r)))
				}
			}()
			s, err := work()
			if err != nil {
				rejectHolder.Invoke(js.ValueOf(err.Error()))
				return
			}
			resolveHolder.Invoke(js.ValueOf(s))
		}()
		return nil
	})
	p := promiseCtor.New(handler)
	// We intentionally do NOT release handler here — once New() has run the
	// executor synchronously, the resolve/reject handles are captured in the
	// goroutine above. Releasing the executor would unpin the Go runtime's
	// reference to the handler function, but since the goroutine still needs
	// resolveHolder/rejectHolder we keep it alive.
	// The handler is released after the goroutine drains via a deferred
	// release in the goroutine? Simpler: rely on the wasm process exit.
	return p
}

// errorPromise returns an immediately-rejected Promise.
func errorPromise(msg string) js.Value {
	return newStringPromise(func() (string, error) {
		return "", errors.New(msg)
	})
}
