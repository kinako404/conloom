package memory

import "strings"

// consMaxParenName bounds the text between brackets read as a name in "P1（名字）".
const consMaxParenName = 40

// consResolveRefs replaces the batch codes in the summary, notes and reasons
// of out with the members' names (see consReplaceRefs) and cuts them to
// their storage limits again.
func consResolveRefs(out modelOutput, names map[string]string) modelOutput {
	resolved := modelOutput{Summary: consTruncate(consReplaceRefs(out.Summary, names), SummaryMaxRunes)}
	for _, m := range out.Members {
		m.Note = consTruncate(consReplaceRefs(m.Note, names), NoteMaxRunes)
		m.Reason = consTruncate(consReplaceRefs(m.Reason, names), consReasonMaxRunes)
		resolved.Members = append(resolved.Members, m)
	}
	return resolved
}

// consReplaceRefs replaces each code of names in s (such as "P1") with its
// name: "名字（P1）" keeps just the name, "P1（名字）" becomes the name, a code
// in brackets after anything else becomes the name in brackets, and a bare
// code becomes the name. Full- and half-width brackets both count. A code
// matches only whole, so "P1" never matches in "P12" or "MP1"; codes missing
// from names are left as they are.
func consReplaceRefs(s string, names map[string]string) string {
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		if consOpenBracket(r[i]) {
			if ref, end, ok := consBracketedCode(r, i); ok {
				if name, known := names[ref]; known {
					if !strings.HasSuffix(b.String(), name) {
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
		b.WriteString(name)
		i = end
		if inner, after, ok := consBracketed(r, i); ok && strings.TrimSpace(inner) == name {
			i = after
		}
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
