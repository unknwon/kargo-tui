// Command kargo-probe POSTs the same RPC twice — once as Connect-RPC over
// JSON (what the TUI uses today) and once as Connect-RPC over binary
// protobuf — and reports whether metav1.Time values come through populated
// in the proto path. Confirms whether the timestamps-are-`{}` problem is a
// JSON-encoder bug we can route around by switching transports, or a
// server-side data-loss bug that no client can fix.
//
// Run via: go run ./cmd/kargo-probe ListStages
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"unknwon.dev/kargo-tui/internal/config"
	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

const servicePath = "/akuity.io.kargo.service.v1alpha1.KargoService/"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kargo-probe <Method> [project] [stage]")
		os.Exit(2)
	}
	method := os.Args[1]
	project := ""
	stage := ""
	if len(os.Args) >= 3 {
		project = os.Args[2]
	}
	if len(os.Args) >= 4 {
		stage = os.Args[3]
	}

	cfg, err := config.Load()
	check(err, "load config")
	ctx := currentContext(cfg)
	if ctx == nil {
		die("no current context in config")
	}
	if project == "" {
		project = ctx.Project
	}
	fmt.Printf("Probing %s · %s · project=%q stage=%q\n\n", ctx.Name, ctx.APIAddress, project, stage)

	httpClient := buildClient(ctx.InsecureSkipTLSVerify)

	jsonResp, jsonStatus, err := postJSON(httpClient, ctx, method, project, stage)
	if err != nil {
		fmt.Printf("JSON path failed: %v\n", err)
	} else {
		fmt.Printf("JSON path: HTTP %d, %d bytes\n", jsonStatus, len(jsonResp))
		analyzeJSONTimestamps(jsonResp)
	}

	fmt.Println()

	protoResp, protoStatus, err := postProto(httpClient, ctx, method, project, stage)
	if err != nil {
		fmt.Printf("Proto path failed: %v\n", err)
		return
	}
	fmt.Printf("Proto path: HTTP %d, %d bytes\n", protoStatus, len(protoResp))
	if protoStatus != 200 {
		fmt.Printf("response body: %s\n", string(protoResp[:min(400, len(protoResp))]))
		return
	}
	analyzeProtoTimestamps(protoResp)

	// End-to-end: decode with the vendored Kargo types and report a real
	// timestamp from a known field. Only for ListStages today; other
	// methods would need their own response message wired in.
	if method == "ListStages" {
		var resp svcv1alpha1.ListStagesResponse
		if err := proto.Unmarshal(protoResp, &resp); err != nil {
			fmt.Printf("typed decode failed: %v\n", err)
			return
		}
		fmt.Printf("\nTyped decode of ListStagesResponse: %d stages\n", len(resp.GetStages()))
		for _, s := range resp.GetStages() {
			ct := s.ObjectMeta.CreationTimestamp
			if ct.IsZero() {
				continue
			}
			fmt.Printf("  %s — created %s\n", s.ObjectMeta.Name, ct.UTC().Format(time.RFC3339))
			break
		}
	}
}

func currentContext(c *config.Config) *config.Context {
	for _, ctx := range c.Contexts {
		if ctx.Name == c.CurrentContext {
			return ctx
		}
	}
	return nil
}

func buildClient(insecure bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

func postJSON(c *http.Client, ctx *config.Context, method, project, stage string) ([]byte, int, error) {
	type req struct {
		Project string `json:"project,omitempty"`
		Stage   string `json:"stage,omitempty"`
	}
	body, _ := json.Marshal(req{Project: project, Stage: stage})
	r, _ := http.NewRequest("POST", strings.TrimRight(ctx.APIAddress, "/")+servicePath+method, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Connect-Protocol-Version", "1")
	if ctx.BearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+ctx.BearerToken)
	}
	resp, err := c.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

func postProto(c *http.Client, ctx *config.Context, method, project, stage string) ([]byte, int, error) {
	body := encodeReq(project, stage)
	r, _ := http.NewRequest("POST", strings.TrimRight(ctx.APIAddress, "/")+servicePath+method, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/proto")
	r.Header.Set("Accept", "application/proto")
	r.Header.Set("Connect-Protocol-Version", "1")
	if ctx.BearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+ctx.BearerToken)
	}
	resp, err := c.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// encodeReq builds a protobuf message body containing field 1 = project
// (when non-empty) and field 2 = stage (when non-empty). Covers every
// Kargo unary RPC the TUI uses.
func encodeReq(project, stage string) []byte {
	var buf []byte
	if project != "" {
		buf = appendString(buf, 1, project)
	}
	if stage != "" {
		buf = appendString(buf, 2, stage)
	}
	return buf
}

func appendString(buf []byte, fieldNum int, value string) []byte {
	tag := uint64(fieldNum)<<3 | 2
	buf = appendVarint(buf, tag)
	buf = appendVarint(buf, uint64(len(value)))
	return append(buf, value...)
}

func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

// analyzeJSONTimestamps walks the JSON looking for "creationTimestamp",
// "startedAt", "finishedAt" and reports how many decode to non-zero values.
func analyzeJSONTimestamps(body []byte) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		fmt.Printf("  decode failed: %v\n", err)
		return
	}
	stats := map[string]struct{ total, populated, samples int }{}
	samplesByField := map[string][]string{}
	walkJSON(v, func(key string, val any) {
		if !isTimeKey(key) {
			return
		}
		s := stats[key]
		s.total++
		populated, repr := classifyJSONTime(val)
		if populated {
			s.populated++
			if s.samples < 1 {
				samplesByField[key] = append(samplesByField[key], repr)
				s.samples++
			}
		}
		stats[key] = s
	})
	if len(stats) == 0 {
		fmt.Println("  no timestamp fields seen")
		return
	}
	for _, k := range sortedKeys(stats) {
		s := stats[k]
		fmt.Printf("  %s: %d/%d populated", k, s.populated, s.total)
		if samples := samplesByField[k]; len(samples) > 0 {
			fmt.Printf("  e.g. %s", samples[0])
		}
		fmt.Println()
	}
}

func isTimeKey(k string) bool {
	switch k {
	case "creationTimestamp", "startedAt", "finishedAt", "lastTransitionTime",
		"firstTimestamp", "lastTimestamp", "startTime", "finishTime",
		"verifiedAt", "approvedAt", "discoveredAt", "createdAt", "creatorDate", "since":
		return true
	}
	return false
}

// classifyJSONTime returns (populated, humanRepr).
func classifyJSONTime(v any) (bool, string) {
	switch x := v.(type) {
	case nil:
		return false, "null"
	case string:
		if x == "" {
			return false, "\"\""
		}
		return true, fmt.Sprintf("%q", x)
	case map[string]any:
		if len(x) == 0 {
			return false, "{}"
		}
		secs, _ := numAsInt64(x["seconds"])
		nanos, _ := numAsInt64(x["nanos"])
		if secs == 0 && nanos == 0 {
			return false, "{seconds:0,nanos:0}"
		}
		return true, fmt.Sprintf("{seconds:%d,nanos:%d}", secs, nanos)
	}
	return false, fmt.Sprintf("%v", v)
}

func numAsInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case float64:
		return int64(x), true
	case string:
		if x == "" {
			return 0, false
		}
		var n int64
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err == nil
	}
	return 0, false
}

func walkJSON(v any, fn func(key string, val any)) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			fn(k, vv)
			walkJSON(vv, fn)
		}
	case []any:
		for _, vv := range x {
			walkJSON(vv, fn)
		}
	}
}

func sortedKeys(m map[string]struct{ total, populated, samples int }) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// analyzeProtoTimestamps scans raw protobuf bytes for nested messages whose
// shape matches metav1.Time ({seconds:int64, nanos:int32}). Reports both
// the count of such messages and how many had non-zero seconds. We don't
// have the message descriptor — we just scan the wire format directly,
// looking for length-delimited submessages of size 2-22 bytes that decode
// as varint(field=1,wire=0,seconds) [+ optional varint(field=2,wire=0,nanos)].
func analyzeProtoTimestamps(body []byte) {
	timeMessages := 0
	populated := 0
	var firstSample string

	scan(body, func(sub []byte) {
		secs, nanos, ok := decodeTimeShape(sub)
		if !ok {
			return
		}
		timeMessages++
		if secs != 0 || nanos != 0 {
			populated++
			if firstSample == "" {
				firstSample = fmt.Sprintf("seconds=%d nanos=%d (%s)",
					secs, nanos, time.Unix(secs, int64(nanos)).UTC().Format(time.RFC3339))
			}
		}
	})

	fmt.Printf("  Time-shaped submessages: %d total, %d populated\n", timeMessages, populated)
	if firstSample != "" {
		fmt.Printf("  e.g. %s\n", firstSample)
	}
	if timeMessages > 0 && populated == 0 {
		fmt.Println("  → server returned Time messages but every (seconds,nanos)=(0,0)")
		fmt.Println("    confirms data is missing from the wire, not a JSON-encoder bug")
	}
	if populated > 0 {
		fmt.Println("  → proto path returned real timestamps; switching transports would fix this")
	}
}

// scan iterates every length-delimited submessage at every nesting depth and
// invokes fn with the inner bytes. Best-effort: malformed bytes terminate
// the current recursion silently.
func scan(data []byte, fn func([]byte)) {
	for len(data) > 0 {
		tag, n := readVarint(data)
		if n == 0 {
			return
		}
		data = data[n:]
		wire := tag & 7
		switch wire {
		case 0: // varint
			_, n = readVarint(data)
			if n == 0 {
				return
			}
			data = data[n:]
		case 1: // 64-bit
			if len(data) < 8 {
				return
			}
			data = data[8:]
		case 2: // length-delimited
			length, ln := readVarint(data)
			if ln == 0 {
				return
			}
			data = data[ln:]
			if uint64(len(data)) < length {
				return
			}
			sub := data[:length]
			data = data[length:]
			fn(sub)
			scan(sub, fn) // recurse
		case 5: // 32-bit
			if len(data) < 4 {
				return
			}
			data = data[4:]
		default:
			return
		}
	}
}

func readVarint(data []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, b := range data {
		if i >= 10 {
			return 0, 0
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
	}
	return 0, 0
}

// decodeTimeShape returns (seconds, nanos, true) if data looks like a
// metav1.Time wire encoding: only fields 1 (varint) and 2 (varint), nothing
// else. We allow either to be missing (default 0).
func decodeTimeShape(data []byte) (int64, int32, bool) {
	if len(data) > 22 {
		return 0, 0, false
	}
	var (
		seconds       int64
		nanos         int32
		seenAnyField  bool
		seenForbidden bool
	)
	d := data
	for len(d) > 0 {
		tag, n := readVarint(d)
		if n == 0 {
			return 0, 0, false
		}
		d = d[n:]
		field := tag >> 3
		wire := tag & 7
		if wire != 0 {
			seenForbidden = true
			break
		}
		v, n := readVarint(d)
		if n == 0 {
			return 0, 0, false
		}
		d = d[n:]
		switch field {
		case 1:
			seconds = int64(v)
			seenAnyField = true
		case 2:
			nanos = int32(v)
			seenAnyField = true
		default:
			seenForbidden = true
		}
	}
	if seenForbidden || !seenAnyField && len(data) > 0 {
		// Empty submessage ({}) also matches Time-shape; useful to count
		// because it tells us "Time fields are present in the schema".
		return 0, 0, len(data) == 0
	}
	if !seenAnyField {
		return 0, 0, true
	}
	return seconds, nanos, true
}

func check(err error, ctx string) {
	if err != nil {
		die("%s: %v", ctx, err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
