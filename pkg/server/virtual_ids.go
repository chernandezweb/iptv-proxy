package server

import (
	"strconv"
	"strings"
)

const (
	// StreamIDMask reserves 26 bits for the original provider stream/category ID (up to 67,108,863).
	StreamIDMask = 0x03FFFFFF
	// ProviderShift shifts the provider index into bits 26..30 (positive 31-bit integer).
	ProviderShift = 26
	// ProviderMask allows up to 31 distinct upstream providers (0..31).
	ProviderMask = 0x1F
)

// EncodeVirtualStreamID encodes a provider index and an original integer stream ID into a collision-free 31-bit integer.
func EncodeVirtualStreamID(provIdx int, origNum int) int {
	if provIdx <= 0 {
		return origNum
	}
	return ((provIdx & ProviderMask) << ProviderShift) | (origNum & StreamIDMask)
}

// DecodeVirtualStreamID decodes a virtual integer ID into provider index and original ID.
// Supports both the 31-bit bit-shifted format and the legacy (provIdx * 1,000,000) format.
func DecodeVirtualStreamID(num int, maxProviders int) (provIdx int, origNum int, ok bool) {
	if num <= 0 {
		return 0, num, false
	}

	// 1. Bit-shifted format (num >= 67,108,864)
	if num >= (1 << ProviderShift) {
		idx := (num >> ProviderShift) & ProviderMask
		orig := num & StreamIDMask
		if idx >= 0 && (maxProviders <= 0 || idx < maxProviders) {
			return idx, orig, true
		}
	}

	// 2. Legacy fallback for old cached playlists: (provIdx * 1,000,000) + id
	if num >= 1000000 {
		idx := num / 1000000
		orig := num % 1000000
		if idx >= 0 && (maxProviders <= 0 || idx < maxProviders) {
			return idx, orig, true
		}
	}

	return 0, num, false
}

// EncodeVirtualCategoryID encodes a provider index and an original integer category ID.
func EncodeVirtualCategoryID(provIdx int, origNum int) int {
	if provIdx <= 0 {
		return origNum
	}
	return ((provIdx & ProviderMask) << ProviderShift) | (origNum & StreamIDMask)
}

// DecodeVirtualCategoryID decodes a virtual category ID string.
// Supports bit-shifted format, legacy (provIdx * 100,000) format, and p<idx>_<id> string format.
func DecodeVirtualCategoryID(catParam string, maxProviders int) (provIdx int, origCatID string) {
	catParam = strings.TrimSpace(catParam)
	if catParam == "" {
		return 0, ""
	}

	// Check alphanumeric prefix format: p<index>_<id>
	if strings.HasPrefix(catParam, "p") && strings.Contains(catParam, "_") {
		parts := strings.SplitN(catParam, "_", 2)
		if idx, err := strconv.Atoi(parts[0][1:]); err == nil && (maxProviders <= 0 || idx < maxProviders) {
			return idx, parts[1]
		}
	}

	if num, errParse := strconv.Atoi(catParam); errParse == nil {
		if num >= (1 << ProviderShift) {
			idx := (num >> ProviderShift) & ProviderMask
			orig := num & StreamIDMask
			if maxProviders <= 0 || idx < maxProviders {
				return idx, strconv.Itoa(orig)
			}
		} else if num >= 100000 {
			// Legacy fallback for categories (provIdx * 100,000)
			idx := num / 100000
			orig := num % 100000
			if maxProviders <= 0 || idx < maxProviders {
				return idx, strconv.Itoa(orig)
			}
		}
		return 0, catParam
	}

	return 0, catParam
}
