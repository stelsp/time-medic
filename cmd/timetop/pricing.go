// Money is the one number this tool refuses to invent. Rates come from the
// installed claude CLI's own embedded catalog — the same table it bills you
// with — or from a price list you point at. Never from a hardcoded guess that
// goes stale on the next launch day.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Rates are dollars per million tokens, except WebSearch which is per request.
// The shape mirrors the CLI's own pricing tiers so nothing is lost in
// translation: cache writes are priced by how long they live.
type Rates struct {
	In         float64 `json:"in"`
	Out        float64 `json:"out"`
	CacheWrite float64 `json:"cache_write_5m"`
	CacheW1h   float64 `json:"cache_write_1h"`
	CacheRead  float64 `json:"cache_read"`
	WebSearch  float64 `json:"web_search"`
}

// PriceTable maps a model id to its rates. Lookup falls back to the longest
// matching prefix, so "claude-opus-5-20260114" finds "claude-opus-5".
type PriceTable struct {
	rates  map[string]Rates
	Source string // where the numbers came from, printed with every dollar
}

func (pt PriceTable) Known() bool { return len(pt.rates) > 0 }

// fastRates: fast mode is billed at its own rates, above whatever tier the
// model normally sits in. The CLI carries the same two exceptions.
var fastRates = map[string]Rates{
	"claude-opus-4-8": {In: 10, Out: 50, CacheWrite: 12.5, CacheW1h: 20, CacheRead: 1, WebSearch: 0.01},
	"claude-opus-5":   {In: 10, Out: 50, CacheWrite: 12.5, CacheW1h: 20, CacheRead: 1, WebSearch: 0.01},
	"claude-opus-4-6": {In: 30, Out: 150, CacheWrite: 37.5, CacheW1h: 60, CacheRead: 3, WebSearch: 0.01},
	"claude-opus-4-7": {In: 30, Out: 150, CacheWrite: 37.5, CacheW1h: 60, CacheRead: 3, WebSearch: 0.01},
}

// usGeoSurcharge is what the CLI adds for US-pinned inference.
const usGeoSurcharge = 1.1

// Cost prices one bucket of tokens. The bucket carries the model, the speed
// and the inference region, because all three change the rate. The second
// return says whether the model was priced at all: an unpriced model must
// never silently cost zero.
func (pt PriceTable) Cost(bucket string, t Tokens) (float64, bool) {
	model, speed, geo := splitBucket(bucket)
	r, ok := pt.ratesFor(model, speed)
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	// a cache write lives 5 minutes unless it was created with the 1h TTL
	w1h := min(t.CacheW1h, t.CacheW)
	w5m := t.CacheW - w1h
	cost := float64(t.In)/perMillion*r.In +
		float64(t.Out)/perMillion*r.Out +
		float64(t.CacheR)/perMillion*r.CacheRead +
		float64(w1h)/perMillion*r.CacheW1h +
		float64(w5m)/perMillion*r.CacheWrite
	if geo == "us" {
		cost *= usGeoSurcharge
	}
	// web search is billed per request and never scaled by region
	return cost + float64(t.WebSearch)*r.WebSearch, true
}

func (pt PriceTable) ratesFor(model, speed string) (Rates, bool) {
	model = canonicalModel(model)
	if speed == "fast" {
		if r, ok := fastRates[model]; ok {
			return r, true
		}
		for id, r := range fastRates {
			if strings.HasPrefix(model, id) {
				return r, true
			}
		}
	}
	if r, ok := pt.rates[model]; ok {
		return r, true
	}
	// only a longer id extending the reported one is a safe match: a truncated
	// id could match several families whose rates differ threefold
	best, bestID := Rates{}, ""
	for id, r := range pt.rates {
		if !strings.HasPrefix(model, id) {
			continue
		}
		if len(id) > len(bestID) || (len(id) == len(bestID) && id < bestID) {
			best, bestID = r, id
		}
	}
	return best, bestID != ""
}

// canonicalModel strips the context-window suffix the CLI appends to a model
// id ("claude-opus-5[1m]"), which is a billing hint, not a different model.
func canonicalModel(model string) string {
	if i := strings.IndexByte(model, '['); i > 0 {
		return model[:i]
	}
	return model
}

// Models lists the priced models, for `timetop prices`.
func (pt PriceTable) Models() []string {
	out := make([]string, 0, len(pt.rates))
	for id := range pt.rates {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (pt PriceTable) Rates(model string) Rates {
	r, _ := pt.ratesFor(model, "")
	return r
}

// LoadPrices resolves the price table. An explicit config wins, otherwise the
// installed CLI's own catalog is used; an empty table is a normal state and
// reports then simply show tokens and no dollars.
func LoadPrices(cfg Config) PriceTable {
	if cfg.PricesFile != "" {
		pt, err := pricesFromFile(cfg.PricesFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "prices:", err)
		} else if pt.Known() {
			return pt
		}
	}
	if cfg.Prices != "" {
		if pt := pricesFromConfig(cfg.Prices); pt.Known() {
			return pt
		}
	}
	if pt, err := pricesFromCLI(cfg); err == nil && pt.Known() {
		return pt
	}
	return PriceTable{}
}

// pricesFromFile reads either LiteLLM's model_prices_and_context_window.json
// (dollars per single token) or a plain {"model": {"in": 15, "out": 75}} table
// (dollars per million). The shape is detected, not configured.
func pricesFromFile(path string) (PriceTable, error) {
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return PriceTable{}, err
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return PriceTable{}, fmt.Errorf("%s: %w", path, err)
	}
	pt := PriceTable{rates: map[string]Rates{}, Source: path}
	for model, fields := range raw {
		if r, ok := ratesFromFields(fields); ok {
			pt.rates[model] = r
		}
	}
	if !pt.Known() {
		return pt, fmt.Errorf("%s: no model in this file carries prices", path)
	}
	return pt, nil
}

func ratesFromFields(f map[string]any) (Rates, bool) {
	num := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			switch n := f[k].(type) {
			case float64:
				return n, true
			case string:
				if parsed, err := strconv.ParseFloat(n, 64); err == nil {
					return parsed, true
				}
			}
		}
		return 0, false
	}
	// LiteLLM: dollars per single token
	if in, ok := num("input_cost_per_token"); ok {
		const perMillion = 1_000_000.0
		out, _ := num("output_cost_per_token")
		cr, _ := num("cache_read_input_token_cost")
		cw, _ := num("cache_creation_input_token_cost")
		return Rates{In: in * perMillion, Out: out * perMillion,
			CacheRead: cr * perMillion, CacheWrite: cw * perMillion,
			CacheW1h: cw * perMillion}, true
	}
	// plain: dollars per million tokens
	if in, ok := num("in", "input", "input_per_mtok"); ok {
		out, _ := num("out", "output", "output_per_mtok")
		cr, _ := num("cache_read", "cacheRead")
		cw, _ := num("cache_write", "cacheWrite", "cache_creation")
		w1h, ok1h := num("cache_write_1h")
		if !ok1h {
			w1h = cw
		}
		return Rates{In: in, Out: out, CacheRead: cr, CacheWrite: cw, CacheW1h: w1h}, true
	}
	return Rates{}, false
}

// pricesFromConfig parses the one-line form:
// PRICES=claude-opus-5:5/25/0.5/6.25,claude-fable-5:10/50/1/12.5
// fields are dollars per million tokens: in/out/cache-read/cache-write.
func pricesFromConfig(s string) PriceTable {
	pt := PriceTable{rates: map[string]Rates{}, Source: "PRICES in config.env"}
	for _, entry := range strings.Split(s, ",") {
		model, nums, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			continue
		}
		f := strings.Split(nums, "/")
		if len(f) < 2 {
			continue
		}
		val := func(i int) float64 {
			if i >= len(f) {
				return 0
			}
			v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v
		}
		cw := val(3)
		pt.rates[strings.TrimSpace(model)] = Rates{
			In: val(0), Out: val(1), CacheRead: val(2), CacheWrite: cw, CacheW1h: cw,
		}
	}
	return pt
}

func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return expand(p, home)
}

// money renders a dollar amount at a precision that suits its size.
func money(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("$%.0f", v)
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v > 0:
		return fmt.Sprintf("$%.3f", v)
	}
	return "$0"
}
