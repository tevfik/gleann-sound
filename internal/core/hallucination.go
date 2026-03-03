package core

import "strings"

// IsHallucination returns true if the text matches known whisper hallucination
// patterns that commonly appear when processing silence or noise.
// Shared across all backends (whisper.cpp, ONNX).
func IsHallucination(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return true
	}
	for _, pattern := range HallucinationPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	// Parenthesized-only output like "(Beyik)" or "(Hepsi)" is a hallucination.
	stripped := strings.TrimSpace(text)
	if isParenthesizedOnly(stripped) {
		return true
	}
	return false
}

// IsRepetitive detects decoder-loop output where a short phrase is repeated
// many times. Works with Turkish/multi-byte text using rune-based matching.
//
// Detects both:
//   - Full repetition: "abc abc abc"
//   - Mid-text repetition: "preamble abc abc abc abc"
//
// Returns true if any substring repeats >=3 consecutive times.
func IsRepetitive(text string) bool {
	runes := []rune(text)
	n := len(runes)
	if n < 12 {
		return false
	}

	maxPatLen := n / 3
	if maxPatLen > 50 {
		maxPatLen = 50
	}

	// Check for repeating patterns starting at each position.
	// We check starts 0..n/2 to catch mid-text repetition.
	maxStart := n / 2
	for start := 0; start <= maxStart; start++ {
		remaining := n - start
		for patLen := 2; patLen <= remaining/3 && patLen <= maxPatLen; patLen++ {
			pat := string(runes[start : start+patLen])
			count := 0
			for i := start; i+patLen <= n; i += patLen {
				if string(runes[i:i+patLen]) == pat {
					count++
				} else {
					break
				}
			}
			if count >= 3 {
				return true
			}
		}
		// Skip ahead to next word boundary to avoid O(n²×m) worst case.
		for start < maxStart && runes[start] != ' ' && runes[start] != ',' {
			start++
		}
	}
	return false
}

// IsSameAsLast checks if the new text is substantially the same as the
// previous text (ignoring minor variations). Used to detect stuck decoder loops
// across consecutive pipeline windows.
func IsSameAsLast(prevText, newText string) bool {
	prev := strings.TrimSpace(prevText)
	curr := strings.TrimSpace(newText)
	if prev == "" || curr == "" {
		return false
	}
	// Exact match.
	if prev == curr {
		return true
	}
	// One is a prefix of the other (decoder producing same text + extras).
	if strings.HasPrefix(curr, prev) || strings.HasPrefix(prev, curr) {
		return true
	}
	return false
}

// isParenthesizedOnly checks if text consists entirely of parenthesized tokens
// like "(Beyik)" or "(Hepsi) (Alkış)" with optional whitespace between.
func isParenthesizedOnly(text string) bool {
	if len(text) < 3 {
		return false
	}
	s := strings.TrimSpace(text)
	if s == "" || s[0] != '(' {
		return false
	}
	depth := 0
	foundAny := false
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				foundAny = true
			}
		case ' ', '\t':
			// whitespace between tokens is ok
		default:
			if depth == 0 {
				return false // non-paren content outside parens
			}
		}
	}
	return foundAny && depth == 0
}

// HallucinationPatterns lists common whisper hallucination strings that appear
// when the model processes silence, noise, or very short audio. These are
// well-documented in the whisper community across multiple languages.
var HallucinationPatterns = []string{
	// Turkish
	"altyazı",
	"izlediğiniz için teşekkür",
	"teşekkür ederim",
	"abone olmayı unutmayın",
	"abone olun",
	"beğenmeyi unutmayın",
	"bir sonraki videoda",
	"görüşmek üzere",
	"videoyu beğenmeyi",
	// English
	"thank you for watching",
	"thanks for watching",
	"please subscribe",
	"like and subscribe",
	"thank you for listening",
	"see you in the next",
	"don't forget to subscribe",
	// German
	"danke fürs zuschauen",
	"danke für das anschauen",
	"bis zum nächsten",
	// Common patterns (language-agnostic)
	"www.",
	"http",
	"[music]",
	"[müzik]",
	"[applause]",
	"♪",
}
