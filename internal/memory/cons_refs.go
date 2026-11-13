package memory

import "strings"

// consMaxParenName bounds the text between brackets read as a name in "P1（名字）".
const consMaxParenName = 40

// consResolveRefs replaces the batch codes in the summary, notes and reasons
// of out with the members' names (see consReplaceRefs) and cuts them to
// summaryMax, noteMax and consReasonMaxRunes runes.
func consResolveRefs(out modelOutput, names map[string]string, literal map[string]bool, summaryMax, noteMax int) modelOutput {
	resolved := modelOutput{Summary: consTruncate(consReplaceRefs(out.Summary, names, literal), summaryMax)}
	for _, m := range out.Members {
		m.Note = consTruncate(consReplaceRefs(m.Note, names, literal), noteMax)
		m.Reason = consTruncate(consReplaceRefs(m.Reason, names, literal), consReasonMaxRunes)
		resolved.Members = append(resolved.Members, m)
	}
	return resolved
}

// consReplaceRefs replaces each code of names in s (such as "P1") with its
// name: "名字（P1）" and "名字 （P1）" keep just the name, "P1（名字）" becomes
// the name, a code in brackets after anything else becomes the name in
// brackets, and a bare code becomes the name unless it is in literal (codes
// written in the batch's own input, such as a game called P5, which are kept
// as written). Full- and half-width brackets both count. A code matches only
// whole, so "P1" never matches in "P12" or "MP1"; codes missing from names
// are left as they are.
func consReplaceRefs(s string, names map[string]string, literal map[string]bool) string {
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		if consOpenBracket(r[i]) {
			if ref, end, ok := consBracketedCode(r, i); ok {
				if name, known := names[ref]; known {
					before := strings.TrimRight(b.String(), " \u3000")
					if strings.HasSuffix(before, name) {
						b.Reset()
						b.WriteString(before)
					} else {
						b.WriteRune(r[i])
						b.WriteString(name)
						b.WriteRune(r[end-1])
					}
					i = end
					continue
				}
			}
		}
		ref, end := consCodeAt(r, i)
		if ref == "" {
			b.WriteRune(r[i])
			i++
			continue
		}
		name, known := names[ref]
		if !known {
			b.WriteString(ref)
			i = end
			continue
		}
		if inner, after, ok := consBracketed(r, end); ok && strings.TrimSpace(inner) == name {
			b.WriteString(name)
			i = after
			continue
		}
		if literal[ref] {
			b.WriteString(ref)
		} else {
			b.WriteString(name)
		}
		i = end
	}
	return b.String()
}

// consCodeAt returns the code ("P" and digits) starting at r[i] and the index
// after it, or "" when none starts there or it is part of a longer word.
func consCodeAt(r []rune, i int) (string, int) {
	if r[i] != 'P' || (i > 0 && consWordRune(r[i-1])) {
		return "", i
	}
	j := i + 1
	for j < len(r) && r[j] >= '0' && r[j] <= '9' {
		j++
	}
	if j == i+1 || (j < len(r) && consWordRune(r[j])) {
		return "", i
	}
	return string(r[i:j]), j
}

// consBracketedCode reads "（P1）" (spaces allowed inside) at r[i] and returns
// the code and the index after the closing bracket.
func consBracketedCode(r []rune, i int) (string, int, bool) {
	j := consSkipSpaces(r, i+1)
	if j >= len(r) {
		return "", 0, false
	}
	ref, end := consCodeAt(r, j)
	if ref == "" {
		return "", 0, false
	}
	end = consSkipSpaces(r, end)
	if end >= len(r) || !consCloseBracket(r[end]) {
		return "", 0, false
	}
	return ref, end + 1, true
}

// consBracketed reads "（…）" at r[i] and returns the text inside and the index
// after the closing bracket.
func consBracketed(r []rune, i int) (string, int, bool) {
	if i >= len(r) || !consOpenBracket(r[i]) {
		return "", 0, false
	}
	for j := i + 1; j < len(r) && j <= i+consMaxParenName; j++ {
		switch {
		case consCloseBracket(r[j]):
			return string(r[i+1 : j]), j + 1, true
		case consOpenBracket(r[j]) || r[j] == '\n':
			return "", 0, false
		}
	}
	return "", 0, false
}

func consSkipSpaces(r []rune, i int) int {
	for i < len(r) && (r[i] == ' ' || r[i] == '\u3000') {
		i++
	}
	return i
}

func consOpenBracket(c rune) bool  { return c == '（' || c == '(' }
func consCloseBracket(c rune) bool { return c == '）' || c == ')' }

func consWordRune(c rune) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// consLiteralCodes returns the codes ("P" and digits, whole) written in texts.
func consLiteralCodes(texts ...string) map[string]bool {
	codes := map[string]bool{}
	for _, t := range texts {
		r := []rune(t)
		for i := 0; i < len(r); i++ {
			if ref, end := consCodeAt(r, i); ref != "" {
				codes[ref] = true
				i = end - 1
			}
		}
	}
	return codes
}
