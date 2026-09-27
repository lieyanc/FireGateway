package importer

import (
	"errors"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
)

var (
	allV4 = netip.MustParsePrefix("0.0.0.0/0")
	allV6 = netip.MustParsePrefix("::/0")
)

// maxPatternPrefixes bounds how many prefixes one wildcard pattern may expand to.
const maxPatternPrefixes = 256

// patternPrefixes converts a rinetd allow/deny pattern to prefixes. rinetd
// glob-matches patterns against the client's textual address: '?' matches one
// character and '*' any run, including dots. Exact addresses, "*" and IPv4
// patterns whose wildcards stay within octets (or cover all trailing octets,
// like "10.1.*") convert exactly; anything else is rejected.
func patternPrefixes(p string) ([]netip.Prefix, error) {
	if p == "*" {
		return []netip.Prefix{allV4, allV6}, nil
	}
	if !strings.ContainsAny(p, "*?") {
		a, err := netip.ParseAddr(strings.Trim(p, "[]"))
		if err != nil {
			return nil, errors.New("not a valid IP address")
		}
		a = a.Unmap()
		return []netip.Prefix{netip.PrefixFrom(a, a.BitLen())}, nil
	}
	if strings.ContainsAny(p, ":[]") {
		return nil, errors.New("IPv6 wildcard patterns cannot be converted")
	}
	parts := strings.Split(p, ".")
	n := len(parts)
	for n > 0 && parts[n-1] == "*" {
		n--
	}
	// With fewer than four parts, only trailing "*" parts can absorb the
	// missing dots and still be expressed as a prefix.
	if len(parts) > 4 || (len(parts) < 4 && n == len(parts)) {
		return nil, errors.New("wildcards that span octets cannot be converted")
	}
	combos := [][]byte{{}}
	for _, part := range parts[:n] {
		var vals []byte
		for v := range 256 {
			if ok, _ := path.Match(part, strconv.Itoa(v)); ok {
				vals = append(vals, byte(v))
			}
		}
		if len(vals) == 0 {
			return nil, errors.New("matches no IPv4 address")
		}
		if len(combos)*len(vals) > maxPatternPrefixes {
			return nil, errors.New("too many address blocks to convert")
		}
		next := make([][]byte, 0, len(combos)*len(vals))
		for _, c := range combos {
			for _, v := range vals {
				next = append(next, append(slices.Clone(c), v))
			}
		}
		combos = next
	}
	out := make([]netip.Prefix, len(combos))
	for i, c := range combos {
		var b [4]byte
		copy(b[:], c)
		out[i] = netip.PrefixFrom(netip.AddrFrom4(b), 8*n)
	}
	return aggregate(out), nil
}

// intersect returns the addresses covered by both sets. Two prefixes either
// don't overlap or one contains the other, so the pairwise intersection is
// simply the longer prefix of each overlapping pair.
func intersect(a, b []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range a {
		for _, y := range b {
			if x.Overlaps(y) {
				if x.Bits() >= y.Bits() {
					out = append(out, x)
				} else {
					out = append(out, y)
				}
			}
		}
	}
	return aggregate(out)
}

// subtract returns the addresses in a that are not in d.
func subtract(a, d []netip.Prefix) []netip.Prefix {
	out := slices.Clone(a)
	for _, y := range d {
		var next []netip.Prefix
		for _, x := range out {
			next = append(next, minus(x, y)...)
		}
		out = next
	}
	return aggregate(out)
}

// minus splits x around y, keeping the halves that don't contain y.
func minus(x, y netip.Prefix) []netip.Prefix {
	if !x.Overlaps(y) {
		return []netip.Prefix{x}
	}
	if y.Bits() <= x.Bits() {
		return nil
	}
	var out []netip.Prefix
	for cur := x; cur.Bits() < y.Bits(); {
		lo, hi := halves(cur)
		if lo.Contains(y.Addr()) {
			out, cur = append(out, hi), lo
		} else {
			out, cur = append(out, lo), hi
		}
	}
	return out
}

func halves(p netip.Prefix) (lo, hi netip.Prefix) {
	bits := p.Bits()
	b := p.Addr().AsSlice()
	lo = netip.PrefixFrom(p.Addr(), bits+1)
	b[bits/8] |= 0x80 >> (bits % 8)
	a, _ := netip.AddrFromSlice(b)
	return lo, netip.PrefixFrom(a, bits+1)
}

// aggregate removes duplicates and covered prefixes and merges sibling
// halves, returning a sorted minimal set.
func aggregate(ps []netip.Prefix) []netip.Prefix {
	set := make(map[netip.Prefix]bool, len(ps))
	for _, p := range ps {
		set[p.Masked()] = true
	}
	for changed := true; changed; {
		changed = false
		for p := range set {
			if p.Bits() == 0 {
				continue
			}
			parent := netip.PrefixFrom(p.Addr(), p.Bits()-1).Masked()
			lo, hi := halves(parent)
			if set[lo] && set[hi] {
				delete(set, lo)
				delete(set, hi)
				set[parent] = true
				changed = true
			}
		}
	}
	out := make([]netip.Prefix, 0, len(set))
	for p := range set {
		covered := false
		for q := range set {
			if q != p && q.Bits() < p.Bits() && q.Contains(p.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return out
}

// coversAll reports whether a set spans every IPv4 and IPv6 address.
func coversAll(ps []netip.Prefix) bool {
	return slices.Contains(ps, allV4) && slices.Contains(ps, allV6)
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		if p.IsSingleIP() {
			out[i] = p.Addr().String()
		} else {
			out[i] = p.String()
		}
	}
	return out
}
