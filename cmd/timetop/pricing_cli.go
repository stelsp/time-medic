// The claude CLI ships the price list it bills you with. Reading it there is
// how this tool quotes dollars without ever hardcoding a rate: upgrade the
// CLI and the numbers follow, offline and without a price API.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// the catalog sits in the binary as minified JS: unquoted keys, bare numbers
var (
	catalogAnchor = []byte("schema_version:1,pricing_tiers:")
	tierRe        = regexp.MustCompile(`([a-z0-9_]+):\{input:([0-9.]+),output:([0-9.]+),cache_write_5m:([0-9.]+),cache_write_1h:([0-9.]+),cache_read:([0-9.]+),web_search:([0-9.]+)\}`)
	modelRe       = regexp.MustCompile(`(?s)\{id:"([^"]+)",family:"[^"]+",display_name:"[^"]+".*?pricing:"([a-z0-9_]+)"`)
)

// catalogWindow is how much of the binary after the anchor holds the catalog;
// the real payload is ~21KB and this leaves room for it to grow.
const catalogWindow = 80 << 10

type catalogCache struct {
	Binary  string           `json:"binary"`
	Size    int64            `json:"size"`
	ModTime int64            `json:"mtime"`
	Rates   map[string]Rates `json:"rates"`
}

// pricesFromCLI returns the installed CLI's own catalog, parsed once and then
// cached against the binary's size and mtime — a 200MB scan per report would
// be absurd.
func pricesFromCLI(cfg Config) (PriceTable, error) {
	bin, err := claudeBinary()
	if err != nil {
		return PriceTable{}, err
	}
	st, err := os.Stat(bin)
	if err != nil {
		return PriceTable{}, err
	}
	cachePath := filepath.Join(cfg.StateDir, "prices-cli.json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var c catalogCache
		if json.Unmarshal(data, &c) == nil && c.Binary == bin &&
			c.Size == st.Size() && c.ModTime == st.ModTime().Unix() && len(c.Rates) > 0 {
			return PriceTable{rates: c.Rates, Source: catalogSource(bin)}, nil
		}
	}
	rates, err := scanCatalog(bin)
	if err != nil {
		return PriceTable{}, err
	}
	if data, err := json.Marshal(catalogCache{
		Binary: bin, Size: st.Size(), ModTime: st.ModTime().Unix(), Rates: rates,
	}); err == nil {
		if os.MkdirAll(filepath.Dir(cachePath), 0o755) == nil {
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, data, 0o644) == nil {
				_ = os.Rename(tmp, cachePath)
			}
		}
	}
	return PriceTable{rates: rates, Source: catalogSource(bin)}, nil
}

func catalogSource(bin string) string {
	return "claude " + filepath.Base(bin) + " price catalog"
}

// claudeBinary resolves the real executable behind the `claude` on PATH, or
// the newest installed version when PATH has none.
func claudeBinary() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if st, err := os.Stat(real); err == nil && st.Size() > 1<<20 {
				return real, nil
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".local", "share", "claude", "versions", "*"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no claude CLI found — dollars stay off until one is installed")
	}
	sort.Slice(matches, func(i, j int) bool { return versionLess(matches[j], matches[i]) })
	return matches[0], nil
}

// versionLess orders "2.1.9" before "2.1.10" — a lexical sort would not.
func versionLess(a, b string) bool {
	as, bs := strings.Split(filepath.Base(a), "."), strings.Split(filepath.Base(b), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		if aerr != nil || berr != nil {
			if as[i] != bs[i] {
				return as[i] < bs[i]
			}
			continue
		}
		if ai != bi {
			return ai < bi
		}
	}
	return len(as) < len(bs)
}

// scanCatalog streams the binary looking for the catalog, so a 200MB
// executable never lands in memory at once.
func scanCatalog(bin string) (map[string]Rates, error) {
	f, err := os.Open(bin)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	const chunk = 4 << 20
	buf := make([]byte, chunk+catalogWindow)
	var carry int
	for {
		n, err := io.ReadFull(f, buf[carry:])
		if n == 0 && err != nil {
			break
		}
		window := buf[:carry+n]
		if i := bytes.Index(window, catalogAnchor); i >= 0 {
			seg := window[i:]
			if len(seg) < catalogWindow {
				// the catalog straddles the end of the file: read the rest
				rest, _ := io.ReadAll(f)
				seg = append(append([]byte{}, seg...), rest...)
			}
			return parseCatalog(seg)
		}
		// keep the tail so an anchor split across chunks is still found
		keep := min(len(window), catalogWindow)
		copy(buf, window[len(window)-keep:])
		carry = keep
		if err != nil {
			break
		}
	}
	return nil, fmt.Errorf("%s: no price catalog in this build", filepath.Base(bin))
}

// parseCatalog turns the embedded JS object into rates per model. It refuses a
// partial parse: a half-read table would quote wrong dollars, which is worse
// than quoting none.
func parseCatalog(seg []byte) (map[string]Rates, error) {
	if len(seg) > catalogWindow {
		seg = seg[:catalogWindow]
	}
	text := string(seg)
	tiers := map[string]Rates{}
	for _, m := range tierRe.FindAllStringSubmatch(text, -1) {
		f := func(i int) float64 {
			v, _ := strconv.ParseFloat(m[i], 64)
			return v
		}
		tiers[m[1]] = Rates{
			In: f(2), Out: f(3), CacheWrite: f(4), CacheW1h: f(5),
			CacheRead: f(6), WebSearch: f(7),
		}
	}
	if len(tiers) == 0 {
		return nil, fmt.Errorf("price catalog found but no tiers parsed")
	}
	rates := map[string]Rates{}
	for _, m := range modelRe.FindAllStringSubmatch(text, -1) {
		if r, ok := tiers[m[2]]; ok {
			rates[m[1]] = r
		}
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("price catalog found but no model referenced a tier")
	}
	return rates, nil
}
