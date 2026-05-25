package newmessagepicker

import (
	"sort"
	"strings"
)

// filter rebuilds m.filtered from m.users honoring the current query,
// the current-user exclusion, and the ranking rules:
//
//  1. Match tier (only when query non-empty):
//     0 = prefix on display name OR username
//     1 = substring on display name OR username
//     2 = subsequence on display name OR username
//     Non-matchers are dropped.
//  2. Recency DESC.
//  3. DisplayName ASC (case-insensitive).
//
// Empty query: include all users (minus self) sorted by Recency DESC
// then DisplayName ASC.
func (m *Model) filter() {
	m.filtered = m.filtered[:0]
	q := strings.ToLower(m.query)

	if q == "" {
		for i, u := range m.users {
			if u.ID == m.currentUserID {
				continue
			}
			m.filtered = append(m.filtered, i)
		}
		sort.SliceStable(m.filtered, func(i, j int) bool {
			return m.lessNoQuery(m.filtered[i], m.filtered[j])
		})
		return
	}

	type match struct {
		idx  int
		tier int
	}
	var matches []match
	for i, u := range m.users {
		if u.ID == m.currentUserID {
			continue
		}
		tier, ok := matchTier(u, q)
		if !ok {
			continue
		}
		matches = append(matches, match{idx: i, tier: tier})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		ua, ub := m.users[a.idx], m.users[b.idx]
		if ua.Recency != ub.Recency {
			return ua.Recency > ub.Recency
		}
		return strings.ToLower(ua.DisplayName) < strings.ToLower(ub.DisplayName)
	})

	for _, mm := range matches {
		m.filtered = append(m.filtered, mm.idx)
	}
}

// matchTier returns (tier, true) if u matches q on either its
// DisplayName or its Username. tier 0 = prefix, 1 = substring,
// 2 = subsequence. q is expected to already be lower-cased.
func matchTier(u User, q string) (int, bool) {
	name := strings.ToLower(u.DisplayName)
	handle := strings.ToLower(u.Username)
	if strings.HasPrefix(name, q) || strings.HasPrefix(handle, q) {
		return 0, true
	}
	if strings.Contains(name, q) || strings.Contains(handle, q) {
		return 1, true
	}
	if isSubsequence(name, q) || isSubsequence(handle, q) {
		return 2, true
	}
	return 0, false
}

// isSubsequence reports whether every rune of q appears in s in
// order. Both inputs are expected to already be lower-cased.
func isSubsequence(s, q string) bool {
	qi := 0
	qrunes := []rune(q)
	if len(qrunes) == 0 {
		return true
	}
	for _, r := range s {
		if qi >= len(qrunes) {
			break
		}
		if r == qrunes[qi] {
			qi++
		}
	}
	return qi == len(qrunes)
}

// lessNoQuery is the comparator used when the query is empty:
// Recency DESC, then DisplayName ASC.
func (m *Model) lessNoQuery(ai, bi int) bool {
	a, b := m.users[ai], m.users[bi]
	if a.Recency != b.Recency {
		return a.Recency > b.Recency
	}
	return strings.ToLower(a.DisplayName) < strings.ToLower(b.DisplayName)
}
