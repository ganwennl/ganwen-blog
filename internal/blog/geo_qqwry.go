package blog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const geoMaxBytes = 64 << 20
const geoMaxTextBytes = 1024
const geoMaxRedirects = 32

var errQQWryInvalid = errors.New("invalid QQWry database")

// QQWry is immutable after validation. Its original GBK country/location and
// area/organization strings are retained, without inventing city granularity.
// Format: https://github.com/metowolf/qqwry.dat/blob/main/protocol.md.
type QQWry struct {
	data    []byte
	first   uint32
	records int
}
type QQWryLocation struct{ Location, Area string }
type qqText struct {
	text string
	next uint32
}

func (q *QQWry) Records() int    { return q.records }
func (q *QQWry) Version() string { return "" }

func ParseQQWry(raw []byte) (*QQWry, error) {
	if len(raw) < 15 || len(raw) > geoMaxBytes {
		return nil, errQQWryInvalid
	}
	first, last := binary.LittleEndian.Uint32(raw[:4]), binary.LittleEndian.Uint32(raw[4:8])
	if first < 8 || last < first || (last-first)%7 != 0 || uint64(last)+7 > uint64(len(raw)) {
		return nil, errQQWryInvalid
	}
	q := &QQWry{data: append([]byte(nil), raw...), first: first, records: int((last-first)/7) + 1}
	cache := map[uint32]qqText{}
	var previous uint32
	for index := 0; index < q.records; index++ {
		start, pointer := q.index(index)
		if index > 0 && start <= previous {
			return nil, errQQWryInvalid
		}
		previous = start
		if !q.bound(pointer, 4) || binary.LittleEndian.Uint32(q.data[pointer:pointer+4]) < start {
			return nil, errQQWryInvalid
		}
		if _, err := q.location(pointer+4, cache); err != nil {
			return nil, err
		}
	}
	return q, nil
}
func (q *QQWry) bound(offset uint32, size uint32) bool {
	return offset >= 8 && uint64(offset)+uint64(size) <= uint64(q.first)
}
func (q *QQWry) index(index int) (uint32, uint32) {
	offset := int(q.first) + index*7
	return binary.LittleEndian.Uint32(q.data[offset : offset+4]), qqUint24(q.data[offset+4 : offset+7])
}
func qqUint24(raw []byte) uint32 { return uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 }
func (q *QQWry) text(offset uint32, cache map[uint32]qqText) (qqText, error) {
	var seen [geoMaxRedirects]uint32
	for depth := 0; depth < geoMaxRedirects; depth++ {
		if offset == 0 {
			return qqText{}, nil
		}
		if !q.bound(offset, 1) {
			return qqText{}, errQQWryInvalid
		}
		for _, old := range seen[:depth] {
			if old == offset {
				return qqText{}, errQQWryInvalid
			}
		}
		seen[depth] = offset
		flag := q.data[offset]
		if flag == 1 || flag == 2 {
			if !q.bound(offset, 4) {
				return qqText{}, errQQWryInvalid
			}
			offset = qqUint24(q.data[offset+1 : offset+4])
			continue
		}
		if value, ok := cache[offset]; ok {
			return value, nil
		}
		end := min(int(q.first), int(offset)+geoMaxTextBytes+1)
		length := bytes.IndexByte(q.data[int(offset):end], 0)
		if length < 0 {
			return qqText{}, errQQWryInvalid
		}
		decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(q.data[int(offset) : int(offset)+length])
		if err != nil || bytes.Contains(decoded, []byte("\xef\xbf\xbd")) || !utf8.Valid(decoded) || utf8.RuneCount(decoded) > 512 {
			return qqText{}, errQQWryInvalid
		}
		for _, r := range string(decoded) {
			if unicode.IsControl(r) {
				return qqText{}, errQQWryInvalid
			}
		}
		text := strings.TrimSpace(string(decoded))
		value := qqText{text: text, next: offset + uint32(length) + 1}
		if cache != nil {
			cache[offset] = value
		}
		return value, nil
	}
	return qqText{}, errQQWryInvalid
}
func (q *QQWry) location(offset uint32, cache map[uint32]qqText) (QQWryLocation, error) {
	var seen [geoMaxRedirects]uint32
	for depth := 0; depth < geoMaxRedirects; depth++ {
		if !q.bound(offset, 1) {
			return QQWryLocation{}, errQQWryInvalid
		}
		for _, old := range seen[:depth] {
			if old == offset {
				return QQWryLocation{}, errQQWryInvalid
			}
		}
		seen[depth] = offset
		flag := q.data[offset]
		if flag == 1 {
			if !q.bound(offset, 4) {
				return QQWryLocation{}, errQQWryInvalid
			}
			offset = qqUint24(q.data[offset+1 : offset+4])
			continue
		}
		var country qqText
		var areaOffset uint32
		var err error
		if flag == 2 {
			if !q.bound(offset, 4) {
				return QQWryLocation{}, errQQWryInvalid
			}
			country, err = q.text(qqUint24(q.data[offset+1:offset+4]), cache)
			areaOffset = offset + 4
		} else {
			country, err = q.text(offset, cache)
			areaOffset = country.next
		}
		if err != nil {
			return QQWryLocation{}, err
		}
		area, err := q.text(areaOffset, cache)
		if err != nil {
			return QQWryLocation{}, err
		}
		return QQWryLocation{country.text, area.text}, nil
	}
	return QQWryLocation{}, errQQWryInvalid
}
func (q *QQWry) Lookup(address netip.Addr) (QQWryLocation, bool) {
	address = address.Unmap()
	if !address.Is4() {
		return QQWryLocation{}, false
	}
	ipBytes := address.As4()
	ip := binary.BigEndian.Uint32(ipBytes[:])
	index := sort.Search(q.records, func(i int) bool { start, _ := q.index(i); return start > ip }) - 1
	if index < 0 {
		return QQWryLocation{}, false
	}
	_, pointer := q.index(index)
	if ip > binary.LittleEndian.Uint32(q.data[pointer:pointer+4]) {
		return QQWryLocation{}, false
	}
	result, err := q.location(pointer+4, nil)
	return result, err == nil
}

func geoCountry(location string) string {
	location = strings.NewReplacer("–", "", "—", "", "-", "", " ", "").Replace(location)
	for _, mapping := range []struct{ prefix, code string }{{"中国香港", "HK"}, {"香港", "HK"}, {"中国澳门", "MO"}, {"澳门", "MO"}, {"中国台湾", "TW"}, {"台湾", "TW"}, {"中國香港", "HK"}, {"中國澳門", "MO"}, {"中國台灣", "TW"}, {"台灣", "TW"}, {"荷兰", "NL"}, {"美国", "US"}, {"英国", "GB"}, {"德国", "DE"}, {"法国", "FR"}, {"西班牙", "ES"}, {"意大利", "IT"}, {"加拿大", "CA"}, {"日本", "JP"}, {"韩国", "KR"}, {"新加坡", "SG"}, {"澳大利亚", "AU"}, {"新西兰", "NZ"}, {"瑞典", "SE"}, {"瑞士", "CH"}, {"俄罗斯", "RU"}, {"马来西亚", "MY"}, {"印度尼西亚", "ID"}, {"印度", "IN"}, {"泰国", "TH"}, {"越南", "VN"}, {"巴西", "BR"}, {"中国", "CN"}, {"中國", "CN"}} {
		if strings.HasPrefix(location, mapping.prefix) {
			return mapping.code
		}
	}
	for _, province := range []string{"北京", "天津", "上海", "重庆", "河北", "山西", "辽宁", "吉林", "黑龙江", "江苏", "浙江", "安徽", "福建", "江西", "山东", "河南", "湖北", "湖南", "广东", "海南", "四川", "贵州", "云南", "陕西", "甘肃", "青海", "内蒙古", "广西", "西藏", "宁夏", "新疆"} {
		if strings.HasPrefix(location, province) {
			return "CN"
		}
	}
	return "unknown"
}
