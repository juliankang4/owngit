// Package jsoninput checks that a JSON request says exactly the text it
// decodes to. encoding/json replaces bytes that are not UTF-8, and a \u
// escape of half a UTF-16 surrogate pair, with U+FFFD, so text decoded from
// such a request would differ from the text sent. The API and the MCP
// server refuse a request that fails this check before decoding it.
package jsoninput

import "unicode/utf8"

// Valid reports whether data is UTF-8 and every \u escape inside its strings
// names a character: a high surrogate escape (\uD800 to \uDBFF) must be
// followed at once by a low surrogate escape (\uDC00 to \uDFFF), and a low
// one may not stand alone. Other malformed JSON is left to the decoder,
// which refuses it.
func Valid(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	inString := false
	for index := 0; index < len(data); index++ {
		switch character := data[index]; {
		case !inString:
			inString = character == '"'
		case character == '"':
			inString = false
		case character != '\\':
		case index+1 < len(data) && data[index+1] == 'u':
			code := escaped(data, index)
			switch {
			case code >= 0xDC00 && code <= 0xDFFF:
				return false
			case code >= 0xD800 && code <= 0xDBFF:
				low := escaped(data, index+6)
				if low < 0xDC00 || low > 0xDFFF {
					return false
				}
				index += 11
			default:
				index++
			}
		default:
			// A one-character escape such as \\ or \": skip the character
			// after the backslash.
			index++
		}
	}
	return true
}

// escaped returns the code unit of the \uXXXX escape starting at data[at],
// or -1 when there is none.
func escaped(data []byte, at int) int {
	if at+6 > len(data) || data[at] != '\\' || data[at+1] != 'u' {
		return -1
	}
	code := 0
	for _, digit := range data[at+2 : at+6] {
		switch {
		case digit >= '0' && digit <= '9':
			code = code<<4 | int(digit-'0')
		case digit >= 'a' && digit <= 'f':
			code = code<<4 | int(digit-'a'+10)
		case digit >= 'A' && digit <= 'F':
			code = code<<4 | int(digit-'A'+10)
		default:
			return -1
		}
	}
	return code
}
