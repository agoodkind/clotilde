package mitm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// SnapshotV2Options configures v2 extraction.
type SnapshotV2Options struct {
	UpstreamName    string
	UpstreamVersion string
	// MaxBodyDepth caps recursion when walking nested body fields.
	// Default 3.
	MaxBodyDepth int
	// EnumThreshold is the maximum number of distinct observed
	// values before a header is classified V2HeaderClassFree
	// instead of V2HeaderClassEnum. Default 5.
	EnumThreshold int
	// IncludeUserAgentSubstrings, when non-empty, restricts records
	// to those whose User-Agent header contains at least one of the
	// listed substrings (case-insensitive). Useful for extracting a
	// canonical reference from a transcript that mixes the upstream
	// CLI, our adapter, and other clients sharing the proxy.
	IncludeUserAgentSubstrings []string
	// ExcludeUserAgentSubstrings drops records whose User-Agent
	// matches any listed substring (case-insensitive). Applied after
	// IncludeUserAgentSubstrings.
	ExcludeUserAgentSubstrings []string
}

const (
	defaultMaxBodyDepth  = 3
	defaultEnumThreshold = 5
)

// ExtractSnapshotV2 reads a JSONL transcript and groups records by
// caller flavor. Each flavor becomes a FlavorShape with classified
// headers and nested body sub-shapes.
func ExtractSnapshotV2(path string, opts SnapshotV2Options) (SnapshotV2, error) {
	if opts.MaxBodyDepth <= 0 {
		opts.MaxBodyDepth = defaultMaxBodyDepth
	}
	if opts.EnumThreshold <= 0 {
		opts.EnumThreshold = defaultEnumThreshold
	}
	rawLines, records, err := readCaptureRecordsRaw(path)
	if err != nil {
		return SnapshotV2{}, err
	}
	return buildSnapshotV2(rawLines, records, opts)
}

// WriteSnapshotV2TOML persists a v2 snapshot under dir as
// reference-v2.toml. The filename differs from v1's reference.toml
// so both can coexist for the same upstream.
func WriteSnapshotV2TOML(snap SnapshotV2, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "reference-v2.toml")
	raw, err := toml.Marshal(snap)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// LoadSnapshotV2TOML reads a reference-v2.toml back into a typed
// SnapshotV2.
func LoadSnapshotV2TOML(path string) (SnapshotV2, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SnapshotV2{}, err
	}
	var snap SnapshotV2
	if err := toml.Unmarshal(raw, &snap); err != nil {
		return SnapshotV2{}, err
	}
	return snap, nil
}

// readCaptureRecordsRaw returns both the typed CaptureRecord values
// AND the original raw line bytes. The v2 extractor needs the raw
// bodies (which the typed schema does not expose for HTTP records
// in summary mode) to walk nested sub-shapes.
func readCaptureRecordsRaw(path string) ([][]byte, []CaptureRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var rawLines [][]byte
	var records []CaptureRecord
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec CaptureRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		// Copy line bytes since scanner reuses the buffer.
		copied := make([]byte, len(line))
		copy(copied, line)
		rawLines = append(rawLines, copied)
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, nil, err
	}
	return rawLines, records, nil
}

// rawRequest pairs a parsed JSON map of one captured request with
// its typed CaptureRecord. The v2 extractor needs both: the typed
// record for stable Kind/T fields, and the raw map for the
// request_body.keys / request_headers nested under summary-mode
// keys the typed schema doesn't expose.
type rawRequest struct {
	raw    map[string]any
	record CaptureRecord
}

func buildSnapshotV2(rawLines [][]byte, records []CaptureRecord, opts SnapshotV2Options) (SnapshotV2, error) {
	if len(records) == 0 {
		return SnapshotV2{}, fmt.Errorf("snapshot v2: no records in transcript")
	}

	// Group request records by flavor. Pair each with its raw line so
	// the body walker has the original JSON.
	flavored := map[string][]rawRequest{}
	flavorSigs := map[string]FlavorSignature{}
	earliest := int64(0)

	for i, rec := range records {
		if rec.T > 0 && (earliest == 0 || rec.T < earliest) {
			earliest = rec.T
		}
		if rec.Kind != RecordHTTPRequest {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(rawLines[i], &raw); err != nil {
			continue
		}
		// Synthesize a richer signature using the raw request_body
		// keys (which the proxy emits in summary mode under
		// request_body.keys but the typed schema does not expose).
		sig := classifyRequestRaw(raw, rec)
		if !uaMatches(sig.UserAgent, opts.IncludeUserAgentSubstrings, opts.ExcludeUserAgentSubstrings) {
			continue
		}
		slug := sig.FlavorSlug()
		flavored[slug] = append(flavored[slug], rawRequest{raw: raw, record: rec})
		flavorSigs[slug] = sig
	}

	if len(flavored) == 0 {
		return SnapshotV2{}, fmt.Errorf("snapshot v2: no http_request records to classify")
	}

	snap := SnapshotV2{
		Upstream: V2Upstream{
			Name:        opts.UpstreamName,
			Version:     opts.UpstreamVersion,
			RecordCount: len(records),
		},
	}
	if earliest > 0 {
		snap.Upstream.CapturedAt = time.Unix(earliest, 0).UTC().Format(time.RFC3339)
	}

	// Build one FlavorShape per flavor, sorted by slug for stable
	// output.
	slugs := make([]string, 0, len(flavored))
	for slug := range flavored {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		flav := buildFlavorShape(slug, flavorSigs[slug], flavored[slug], opts)
		snap.Flavors = append(snap.Flavors, flav)
	}

	return snap, nil
}

// uaMatches applies include/exclude substring filters to a User-Agent.
// Empty include set means include-all. Comparisons are case-insensitive.
func uaMatches(ua string, include, exclude []string) bool {
	low := strings.ToLower(ua)
	for _, ex := range exclude {
		if ex == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(ex)) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, inc := range include {
		if inc == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(inc)) {
			return true
		}
	}
	return false
}

// classifyRequestRaw is the v2 classifier. Same signature shape as
// ClassifyRecord but pulls body keys from the raw map (which has the
// summary-mode request_body.keys array the typed CaptureRecord
// schema doesn't expose).
func classifyRequestRaw(raw map[string]any, rec CaptureRecord) FlavorSignature {
	headers, _ := raw["request_headers"].(map[string]any)
	if headers == nil {
		headers, _ = raw["headers"].(map[string]any)
	}
	ua := lowerStringFromAny(headers, "user-agent")
	beta := lowerStringFromAny(headers, "anthropic-beta")
	keys := bodyKeysFromRawMap(raw)
	_ = rec
	return FlavorSignature{
		UserAgent:       ua,
		BetaFingerprint: betaFingerprint(beta),
		BodyKeys:        keys,
	}
}

func lowerStringFromAny(m map[string]any, lower string) string {
	if m == nil {
		return ""
	}
	for k, v := range m {
		if strings.ToLower(k) == lower {
			s, _ := v.(string)
			return s
		}
	}
	return ""
}

// bodyKeysFromRawMap extracts the list of top-level body keys from a
// captured request record's raw JSON map. Handles both summary mode
// (request_body.keys array) and raw mode (request_body is the full
// object).
func bodyKeysFromRawMap(raw map[string]any) []string {
	body := raw["request_body"]
	switch b := body.(type) {
	case map[string]any:
		if keys, ok := b["keys"].([]any); ok {
			out := make([]string, 0, len(keys))
			for _, v := range keys {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
			sort.Strings(out)
			return out
		}
		// Raw mode (legacy shape where the proxy decoded JSON before
		// recording): the body IS the full payload object.
		out := make([]string, 0, len(b))
		for k := range b {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	case string:
		// Raw mode: the proxy stores the JSON payload as a string.
		// Parse it on the fly so the classifier sees real keys
		// instead of clustering every raw-mode request as "other".
		var parsed map[string]any
		if err := json.Unmarshal([]byte(b), &parsed); err != nil {
			return nil
		}
		out := make([]string, 0, len(parsed))
		for k := range parsed {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

func buildFlavorShape(slug string, sig FlavorSignature, requests []rawRequest, opts SnapshotV2Options) FlavorShape {
	flav := FlavorShape{
		Slug:        slug,
		Signature:   V2Signature{UserAgent: sig.UserAgent, BetaFingerprint: sig.BetaFingerprint, BodyKeys: sig.BodyKeys},
		RecordCount: len(requests),
	}

	methodSet := map[string]bool{}
	pathSet := map[string]bool{}
	headerObservations := map[string]map[string]int{} // header → value → count
	headerPresenceCount := map[string]int{}            // header → number of records present
	bodyTypeSet := map[string]bool{}
	fieldObservations := map[string]*v2FieldAcc{} // top-level body field → aggregated observation

	for _, req := range requests {
		if m := lowerStringFromAny(req.raw, "method"); m != "" {
			methodSet[m] = true
		}
		if p := lowerStringFromAny(req.raw, "path"); p != "" {
			pathSet[p] = true
		}
		headers := mapStringsFromAny(req.raw["request_headers"])
		if len(headers) == 0 {
			headers = mapStringsFromAny(req.raw["headers"])
		}
		seenInThisRecord := map[string]bool{}
		for k, v := range headers {
			lower := strings.ToLower(k)
			seenInThisRecord[lower] = true
			if headerObservations[lower] == nil {
				headerObservations[lower] = map[string]int{}
			}
			headerObservations[lower][v]++
		}
		for h := range seenInThisRecord {
			headerPresenceCount[h]++
		}
		// Body
		body := req.raw["request_body"]
		if bm, ok := body.(map[string]any); ok {
			if t, ok := bm["body_type"].(string); ok && t != "" {
				bodyTypeSet[t] = true
			}
		}
		walkBodyTopLevel(body, fieldObservations, opts.MaxBodyDepth)
	}

	flav.Methods = sortedStringSet(methodSet)
	flav.Paths = sortedStringSet(pathSet)

	// Headers
	for name, values := range headerObservations {
		flav.Headers = append(flav.Headers, classifyHeader(name, values, headerPresenceCount[name], len(requests), opts))
	}
	sort.Slice(flav.Headers, func(i, j int) bool { return flav.Headers[i].Name < flav.Headers[j].Name })

	// Body
	flav.Body.BodyType = strings.Join(sortedStringSet(bodyTypeSet), ",")
	for name, acc := range fieldObservations {
		flav.Body.Fields = append(flav.Body.Fields, acc.materialize(name, len(requests)))
	}
	sort.Slice(flav.Body.Fields, func(i, j int) bool { return flav.Body.Fields[i].Name < flav.Body.Fields[j].Name })

	return flav
}

func mapStringsFromAny(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func sortedStringSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func classifyHeader(name string, values map[string]int, presence, total int, opts SnapshotV2Options) V2Header {
	observed := make([]string, 0, len(values))
	for v := range values {
		observed = append(observed, v)
	}
	sort.Strings(observed)
	classification := V2HeaderClassConstant
	if len(observed) > 1 {
		if len(observed) > opts.EnumThreshold {
			classification = V2HeaderClassFree
		} else {
			classification = V2HeaderClassEnum
		}
	}
	pres := V2HeaderPresenceRequired
	if presence < total {
		pres = V2HeaderPresenceOptional
	}
	rate := 0.0
	if total > 0 {
		rate = float64(presence) / float64(total)
	}
	out := V2Header{
		Name:           name,
		Classification: classification,
		Presence:       pres,
		OccurrenceRate: rate,
		ObservedValues: observed,
	}
	if classification == V2HeaderClassFree {
		out.Pattern = canonicalHeaderValue(name, observed[0])
		out.ObservedValues = nil
	}
	return out
}

// v2FieldAcc accumulates observations for one top-level body field
// across multiple captured requests.
type v2FieldAcc struct {
	count            int
	kindCounts       map[V2FieldKind]int
	sample           string
	subAcc           map[string]*v2FieldAcc
	itemKindCounts   map[V2FieldKind]int
	itemSubAcc       map[string]*v2FieldAcc
}

func newFieldAcc() *v2FieldAcc {
	return &v2FieldAcc{
		kindCounts:     map[V2FieldKind]int{},
		subAcc:         map[string]*v2FieldAcc{},
		itemKindCounts: map[V2FieldKind]int{},
		itemSubAcc:     map[string]*v2FieldAcc{},
	}
}

func (a *v2FieldAcc) observe(value any, depth int) {
	a.count++
	kind := classifyKind(value)
	a.kindCounts[kind]++
	if a.sample == "" {
		a.sample = sampleValue(value)
	}
	if depth <= 0 {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for k, sub := range v {
			child, ok := a.subAcc[k]
			if !ok {
				child = newFieldAcc()
				a.subAcc[k] = child
			}
			child.observe(sub, depth-1)
		}
	case []any:
		for _, item := range v {
			itemKind := classifyKind(item)
			a.itemKindCounts[itemKind]++
			if itemKind == V2FieldKindObject {
				if obj, ok := item.(map[string]any); ok {
					for k, sub := range obj {
						child, ok := a.itemSubAcc[k]
						if !ok {
							child = newFieldAcc()
							a.itemSubAcc[k] = child
						}
						child.observe(sub, depth-1)
					}
				}
			}
		}
	}
}

func (a *v2FieldAcc) materialize(name string, totalRecords int) V2Field {
	out := V2Field{
		Name:           name,
		Kind:           dominantKind(a.kindCounts),
		SampleValue:    a.sample,
	}
	if a.count >= totalRecords {
		out.Presence = V2HeaderPresenceRequired
	} else {
		out.Presence = V2HeaderPresenceOptional
	}
	if totalRecords > 0 {
		out.OccurrenceRate = float64(a.count) / float64(totalRecords)
	}
	if out.Kind == V2FieldKindObject {
		for sub, acc := range a.subAcc {
			out.SubFields = append(out.SubFields, acc.materialize(sub, a.count))
		}
		sort.Slice(out.SubFields, func(i, j int) bool { return out.SubFields[i].Name < out.SubFields[j].Name })
	}
	if out.Kind == V2FieldKindArray {
		out.ItemKind = dominantKind(a.itemKindCounts)
		for sub, acc := range a.itemSubAcc {
			out.ItemSubFields = append(out.ItemSubFields, acc.materialize(sub, a.count))
		}
		sort.Slice(out.ItemSubFields, func(i, j int) bool { return out.ItemSubFields[i].Name < out.ItemSubFields[j].Name })
	}
	return out
}

func walkBodyTopLevel(body any, dst map[string]*v2FieldAcc, maxDepth int) {
	bm, ok := body.(map[string]any)
	if !ok {
		return
	}
	// Special-case: summary-mode body has request_body.keys plus a
	// few summary fields; treat each summary-key as a top-level body
	// field with kind=unknown (we only know names, not values).
	if keys, ok := bm["keys"].([]any); ok {
		for _, k := range keys {
			s, _ := k.(string)
			if s == "" {
				continue
			}
			acc, ok := dst[s]
			if !ok {
				acc = newFieldAcc()
				dst[s] = acc
			}
			acc.count++
			acc.kindCounts[V2FieldKindUnknown]++
		}
		return
	}
	// Raw mode: recurse into the actual body structure.
	for name, value := range bm {
		acc, ok := dst[name]
		if !ok {
			acc = newFieldAcc()
			dst[name] = acc
		}
		acc.observe(value, maxDepth-1)
	}
}

func classifyKind(v any) V2FieldKind {
	switch v.(type) {
	case nil:
		return V2FieldKindNull
	case bool:
		return V2FieldKindBool
	case float64, int, int64, json.Number:
		return V2FieldKindNumber
	case string:
		return V2FieldKindString
	case []any:
		return V2FieldKindArray
	case map[string]any:
		return V2FieldKindObject
	}
	return V2FieldKindUnknown
}

func sampleValue(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) > 200 {
			return fmt.Sprintf("<string len=%d>", len(x))
		}
		return x
	case []any:
		return fmt.Sprintf("<array len=%d>", len(x))
	case map[string]any:
		return fmt.Sprintf("<object keys=%d>", len(x))
	default:
		return fmt.Sprintf("%v", v)
	}
}

func dominantKind(counts map[V2FieldKind]int) V2FieldKind {
	best := V2FieldKindUnknown
	bestCount := 0
	for k, c := range counts {
		if c > bestCount {
			best = k
			bestCount = c
		}
	}
	return best
}
