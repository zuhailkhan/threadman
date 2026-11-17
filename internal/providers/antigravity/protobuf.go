package antigravity

import "unicode/utf8"

// A minimal protobuf wire-format reader: enough to pull known fields out of a
// message without its schema.

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// pbScan calls fn for every top-level field of msg, with the varint value or
// the length-delimited bytes. It stops when fn returns false or the message is
// malformed.
func pbScan(msg []byte, fn func(num int, wire int, v uint64, data []byte) bool) {
	for len(msg) > 0 {
		key, n := readVarint(msg)
		if n == 0 {
			return
		}
		msg = msg[n:]
		num, wire := int(key>>3), int(key&7)
		var (
			v    uint64
			data []byte
		)
		switch wire {
		case wireVarint:
			if v, n = readVarint(msg); n == 0 {
				return
			}
			msg = msg[n:]
		case wireFixed64:
			if len(msg) < 8 {
				return
			}
			msg = msg[8:]
		case wireFixed32:
			if len(msg) < 4 {
				return
			}
			msg = msg[4:]
		case wireBytes:
			size, n := readVarint(msg)
			if n == 0 || size > uint64(len(msg)-n) {
				return
			}
			data = msg[n : n+int(size)]
			msg = msg[n+int(size):]
		default:
			return
		}
		if !fn(num, wire, v, data) {
			return
		}
	}
}

// pbBytes follows a path of length-delimited field numbers into nested
// messages and returns the bytes of the last one.
func pbBytes(msg []byte, path ...int) ([]byte, bool) {
	for _, want := range path {
		var (
			next  []byte
			found bool
		)
		pbScan(msg, func(num, wire int, _ uint64, data []byte) bool {
			if num == want && wire == wireBytes {
				next, found = data, true
				return false
			}
			return true
		})
		if !found {
			return nil, false
		}
		msg = next
	}
	return msg, true
}

// pbString is pbBytes for a string field; it returns "" when the field is
// missing or not valid UTF-8.
func pbString(msg []byte, path ...int) string {
	b, ok := pbBytes(msg, path...)
	if !ok || !utf8.Valid(b) {
		return ""
	}
	return string(b)
}

func pbVarint(msg []byte, want int) (uint64, bool) {
	var (
		out   uint64
		found bool
	)
	pbScan(msg, func(num, wire int, v uint64, _ []byte) bool {
		if num == want && wire == wireVarint {
			out, found = v, true
			return false
		}
		return true
	})
	return out, found
}
