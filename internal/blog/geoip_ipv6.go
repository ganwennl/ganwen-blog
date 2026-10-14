package blog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"
)

// Format reference: itbdw/ip-database, branch 3.x, src/IpParser/IpV6wry.php
// https://github.com/itbdw/ip-database/blob/3.x/src/IpParser/IpV6wry.php
// That project is MIT licensed; its license is archived in
// deploy/geoip-LICENSE-itbdw.txt. This is an independent, bounded Go reader.
const (
	ipv6GeoHeaderBytes  = 40
	ipv6GeoMaxRecords   = 1 << 20
	ipv6GeoMaxTextBytes = 2048
	ipv6GeoMaxTextRunes = 512
	ipv6GeoMaxRedirects = 64
)

var errIPv6GeoInvalid = errors.New("invalid IPv6 GeoIP database")

type ipv6GeoEntry struct {
	high, low uint64
	place     QQWryLocation
}

// IPv6GeoDB is immutable and fully validated before it is returned. The real
// IPv6Wry database indexes the high 64 bits, so all addresses in that /64 use
// the same entry. Compressed 1–16 byte indexes follow the upstream convention
// of little-endian high/low integers with omitted trailing address bits zero.
// Location and Area retain the complete UTF-8 database strings.
type IPv6GeoDB struct {
	entries []ipv6GeoEntry
	version string
}

func (db *IPv6GeoDB) Records() int {
	if db == nil {
		return 0
	}
	return len(db.entries)
}

func (db *IPv6GeoDB) Version() string {
	if db == nil {
		return ""
	}
	return db.version
}

// LoadIPv6GeoDB closes the source file before returning; updates can replace it
// without changing an already loaded database or retaining an open descriptor.
func LoadIPv6GeoDB(path string) (*IPv6GeoDB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open IPv6 GeoIP database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < ipv6GeoHeaderBytes || info.Size() > geoMaxBytes {
		return nil, errIPv6GeoInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open IPv6 GeoIP database: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, geoMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read IPv6 GeoIP database: %w", err)
	}
	return ParseIPv6GeoDB(raw)
}

// ParseIPv6GeoDB validates every indexed record, including redirects that a
// particular lookup would never visit. It does not retain the caller's bytes.
func ParseIPv6GeoDB(raw []byte) (*IPv6GeoDB, error) {
	if len(raw) < ipv6GeoHeaderBytes || len(raw) > geoMaxBytes || string(raw[:4]) != "IPDB" || binary.LittleEndian.Uint16(raw[4:6]) != 1 {
		return nil, errIPv6GeoInvalid
	}
	width, ipBytes := int(raw[6]), int(raw[7])
	if width < 1 || width > 8 || ipBytes < 1 || ipBytes > 16 {
		return nil, errIPv6GeoInvalid
	}
	total := binary.LittleEndian.Uint64(raw[8:16])
	first := binary.LittleEndian.Uint64(raw[16:24])
	stride, size := uint64(width+ipBytes), uint64(len(raw))
	if total == 0 || total > ipv6GeoMaxRecords || first < ipv6GeoHeaderBytes || first > size || total > (size-first)/stride {
		return nil, errIPv6GeoInvalid
	}
	reader := ipv6GeoReader{raw: raw, limit: first, width: width, texts: make(map[uint64]ipv6GeoText), records: make(map[uint64]QQWryLocation)}
	db := &IPv6GeoDB{entries: make([]ipv6GeoEntry, int(total))}
	// The IPDB v1 header stores an optional NUL-terminated edition date pointer.
	if edition := binary.LittleEndian.Uint64(raw[32:40]); edition != 0 {
		version, err := reader.text(edition)
		if err != nil || len(version.value) != 8 {
			return nil, errIPv6GeoInvalid
		}
		if _, err := time.Parse("20060102", version.value); err != nil {
			return nil, errIPv6GeoInvalid
		}
		db.version = version.value
	}
	for i := range db.entries {
		offset := int(first + uint64(i)*stride)
		key := raw[offset : offset+ipBytes]
		entry := &db.entries[i]
		if ipBytes <= 8 {
			entry.high = ipv6GeoUint(key) << uint(8*(8-ipBytes))
		} else {
			entry.high = ipv6GeoUint(key[:8])
			entry.low = ipv6GeoUint(key[8:]) << uint(8*(16-ipBytes))
		}
		if i > 0 {
			previous := db.entries[i-1]
			if entry.high < previous.high || entry.high == previous.high && entry.low <= previous.low {
				return nil, fmt.Errorf("%w: unordered index %d", errIPv6GeoInvalid, i)
			}
		}
		pointer := ipv6GeoUint(raw[offset+ipBytes : offset+ipBytes+width])
		place, err := reader.record(pointer)
		if err != nil {
			return nil, fmt.Errorf("%w: record %d", errIPv6GeoInvalid, i)
		}
		entry.place = place
	}
	return db, nil
}

// Lookup rejects IPv4 and IPv4-mapped addresses. Native 6to4 and other IPv6
// allocations retain the database's original ownership rather than mapping to
// an embedded IPv4 address. The final range includes the maximum IPv6 address.
func (db *IPv6GeoDB) Lookup(address netip.Addr) (QQWryLocation, bool) {
	if db == nil || !address.Is6() || address.Is4In6() || len(db.entries) == 0 {
		return QQWryLocation{}, false
	}
	ip := address.As16()
	high, low := binary.BigEndian.Uint64(ip[:8]), binary.BigEndian.Uint64(ip[8:])
	index := sort.Search(len(db.entries), func(i int) bool {
		entry := db.entries[i]
		return entry.high > high || entry.high == high && entry.low > low
	}) - 1
	if index < 0 {
		return QQWryLocation{}, false
	}
	return db.entries[index].place, true
}

func ipv6GeoUint(raw []byte) uint64 {
	var value uint64
	for i, b := range raw {
		value |= uint64(b) << uint(i*8)
	}
	return value
}

type ipv6GeoText struct {
	value string
	next  uint64
}

type ipv6GeoReader struct {
	raw     []byte
	limit   uint64
	width   int
	texts   map[uint64]ipv6GeoText
	records map[uint64]QQWryLocation
}

func (r *ipv6GeoReader) bound(offset, size uint64) bool {
	return offset >= ipv6GeoHeaderBytes && offset < r.limit && size <= r.limit-offset
}

func (r *ipv6GeoReader) pointer(offset uint64) (uint64, error) {
	if !r.bound(offset, uint64(r.width)) {
		return 0, errIPv6GeoInvalid
	}
	return ipv6GeoUint(r.raw[int(offset) : int(offset)+r.width]), nil
}

func (r *ipv6GeoReader) text(offset uint64) (ipv6GeoText, error) {
	var seen [ipv6GeoMaxRedirects]uint64
	var depth int
	for ; depth < len(seen); depth++ {
		if offset == 0 {
			break // A zero string pointer means an empty area, per upstream.
		}
		if !r.bound(offset, 1) {
			return ipv6GeoText{}, errIPv6GeoInvalid
		}
		for _, old := range seen[:depth] {
			if old == offset {
				return ipv6GeoText{}, errIPv6GeoInvalid
			}
		}
		if value, ok := r.texts[offset]; ok {
			return r.cacheText(seen[:depth], value), nil
		}
		seen[depth] = offset
		flag := r.raw[int(offset)]
		if flag == 2 {
			pointer, err := r.pointer(offset + 1)
			if err != nil {
				return ipv6GeoText{}, err
			}
			offset = pointer
			continue
		}
		if flag == 1 {
			return ipv6GeoText{}, errIPv6GeoInvalid // 0x01 redirects whole records only.
		}
		end := min(r.limit, offset+ipv6GeoMaxTextBytes+1)
		length := bytes.IndexByte(r.raw[int(offset):int(end)], 0)
		if length < 0 {
			return ipv6GeoText{}, errIPv6GeoInvalid
		}
		raw := r.raw[int(offset) : int(offset)+length]
		// Keep complete labels within the analytics/backup storage contract;
		// reject oversized databases at installation rather than truncate them.
		if !utf8.Valid(raw) || utf8.RuneCount(raw) > ipv6GeoMaxTextRunes {
			return ipv6GeoText{}, errIPv6GeoInvalid
		}
		value := string(raw)
		for _, character := range value {
			if unicode.IsControl(character) {
				return ipv6GeoText{}, errIPv6GeoInvalid
			}
		}
		return r.cacheText(seen[:depth+1], ipv6GeoText{value: value, next: offset + uint64(length) + 1}), nil
	}
	if depth == len(seen) {
		return ipv6GeoText{}, errIPv6GeoInvalid
	}
	return r.cacheText(seen[:depth], ipv6GeoText{}), nil
}

func (r *ipv6GeoReader) cacheText(offsets []uint64, result ipv6GeoText) ipv6GeoText {
	for i := len(offsets) - 1; i >= 0; i-- {
		offset := offsets[i]
		if r.raw[int(offset)] == 2 {
			result.next = offset + uint64(r.width) + 1
		}
		r.texts[offset] = result
	}
	return result
}

func (r *ipv6GeoReader) record(offset uint64) (QQWryLocation, error) {
	var seen [ipv6GeoMaxRedirects]uint64
	for depth := 0; depth < len(seen); depth++ {
		if !r.bound(offset, 1) {
			return QQWryLocation{}, errIPv6GeoInvalid
		}
		for _, old := range seen[:depth] {
			if old == offset {
				return QQWryLocation{}, errIPv6GeoInvalid
			}
		}
		if result, ok := r.records[offset]; ok {
			for _, previous := range seen[:depth] {
				r.records[previous] = result
			}
			return result, nil
		}
		seen[depth] = offset
		if r.raw[int(offset)] == 1 {
			pointer, err := r.pointer(offset + 1)
			if err != nil {
				return QQWryLocation{}, err
			}
			offset = pointer
			continue
		}
		location, err := r.text(offset)
		if err != nil {
			return QQWryLocation{}, err
		}
		area, err := r.text(location.next)
		if err != nil {
			return QQWryLocation{}, err
		}
		result := QQWryLocation{Location: location.value, Area: area.value}
		for _, previous := range seen[:depth+1] {
			r.records[previous] = result
		}
		return result, nil
	}
	return QQWryLocation{}, errIPv6GeoInvalid
}
