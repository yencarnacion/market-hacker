package nato

import (
	"strings"
)

var words = map[rune]string{
	'A': "alpha",
	'B': "bravo",
	'C': "charlie",
	'D': "delta",
	'E': "echo",
	'F': "foxtrot",
	'G': "golf",
	'H': "hotel",
	'I': "india",
	'J': "juliett",
	'K': "kilo",
	'L': "lima",
	'M': "mike",
	'N': "november",
	'O': "oscar",
	'P': "papa",
	'Q': "quebec",
	'R': "romeo",
	'S': "sierra",
	'T': "tango",
	'U': "uniform",
	'V': "victor",
	'W': "whiskey",
	'X': "x-ray",
	'Y': "yankee",
	'Z': "zulu",
	'0': "zero",
	'1': "one",
	'2': "two",
	'3': "three",
	'4': "four",
	'5': "five",
	'6': "six",
	'7': "seven",
	'8': "eight",
	'9': "nine",
}

func SpellNATO(ticker string) string {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	parts := make([]string, 0, len(ticker))
	for _, r := range ticker {
		if w, ok := words[r]; ok {
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, ", ")
}
