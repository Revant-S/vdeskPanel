package drivefake

import (
	"fmt"
	"strings"
)

// The q parser accepts only the clauses the client is meant to send. Anything else is a
// 400, so drift in the client's queries fails tests instead of silently matching nothing.

type tokenKind int

const (
	tokenWord tokenKind = iota + 1
	tokenString
	tokenSymbol
)

type token struct {
	kind  tokenKind
	value string
}

type filePredicate func(*storedFile) bool

func parseQuery(query string) (filePredicate, error) {
	tokens, err := tokenizeQuery(query)
	if err != nil {
		return nil, err
	}
	parser := &queryParser{tokens: tokens}
	var predicates []filePredicate
	for {
		predicate, err := parser.parseClause()
		if err != nil {
			return nil, fmt.Errorf("invalid query %q: %w", query, err)
		}
		predicates = append(predicates, predicate)
		if parser.done() {
			break
		}
		if err := parser.expect(tokenWord, "and"); err != nil {
			return nil, fmt.Errorf("invalid query %q: %w", query, err)
		}
	}
	return func(file *storedFile) bool {
		for _, predicate := range predicates {
			if !predicate(file) {
				return false
			}
		}
		return true
	}, nil
}

type queryParser struct {
	tokens   []token
	position int
}

func (p *queryParser) done() bool { return p.position >= len(p.tokens) }

func (p *queryParser) next() (token, error) {
	if p.done() {
		return token{}, fmt.Errorf("unexpected end of query")
	}
	current := p.tokens[p.position]
	p.position++
	return current, nil
}

func (p *queryParser) expect(kind tokenKind, value string) error {
	current, err := p.next()
	if err != nil {
		return err
	}
	if current.kind != kind || current.value != value {
		return fmt.Errorf("expected %q, got %q", value, current.value)
	}
	return nil
}

func (p *queryParser) expectString() (string, error) {
	current, err := p.next()
	if err != nil {
		return "", err
	}
	if current.kind != tokenString {
		return "", fmt.Errorf("expected a quoted string, got %q", current.value)
	}
	return current.value, nil
}

func (p *queryParser) expectAssignedString(field string) (string, error) {
	if err := p.expect(tokenWord, field); err != nil {
		return "", err
	}
	if err := p.expect(tokenSymbol, "="); err != nil {
		return "", err
	}
	return p.expectString()
}

func (p *queryParser) parseClause() (filePredicate, error) {
	first, err := p.next()
	if err != nil {
		return nil, err
	}
	if first.kind == tokenString {
		if err := p.expect(tokenWord, "in"); err != nil {
			return nil, err
		}
		if err := p.expect(tokenWord, "parents"); err != nil {
			return nil, err
		}
		return func(file *storedFile) bool { return file.hasParent(first.value) }, nil
	}
	p.position--
	switch first.value {
	case "name":
		name, err := p.expectAssignedString("name")
		return func(file *storedFile) bool { return file.name == name }, err
	case "mimeType":
		mimeType, err := p.expectAssignedString("mimeType")
		return func(file *storedFile) bool { return file.mimeType == mimeType }, err
	case "trashed":
		return p.parseTrashed()
	case "appProperties":
		return p.parseAppProperty()
	}
	return nil, fmt.Errorf("unsupported clause starting with %q", first.value)
}

func (p *queryParser) parseTrashed() (filePredicate, error) {
	if err := p.expect(tokenWord, "trashed"); err != nil {
		return nil, err
	}
	if err := p.expect(tokenSymbol, "="); err != nil {
		return nil, err
	}
	value, err := p.next()
	if err != nil {
		return nil, err
	}
	if value.kind != tokenWord || (value.value != "true" && value.value != "false") {
		return nil, fmt.Errorf("trashed must be true or false, got %q", value.value)
	}
	want := value.value == "true"
	return func(file *storedFile) bool { return file.trashed == want }, nil
}

func (p *queryParser) parseAppProperty() (filePredicate, error) {
	steps := []func() error{
		func() error { return p.expect(tokenWord, "appProperties") },
		func() error { return p.expect(tokenWord, "has") },
		func() error { return p.expect(tokenSymbol, "{") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	key, err := p.expectAssignedString("key")
	if err != nil {
		return nil, err
	}
	if err := p.expect(tokenWord, "and"); err != nil {
		return nil, err
	}
	value, err := p.expectAssignedString("value")
	if err != nil {
		return nil, err
	}
	if err := p.expect(tokenSymbol, "}"); err != nil {
		return nil, err
	}
	return func(file *storedFile) bool {
		stored, ok := file.properties[key]
		return ok && stored == value
	}, nil
}

func tokenizeQuery(query string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(query); {
		switch char := query[i]; {
		case char == ' ':
			i++
		case char == '=' || char == '{' || char == '}':
			tokens = append(tokens, token{kind: tokenSymbol, value: string(char)})
			i++
		case char == '\'':
			value, length, err := readQuotedString(query[i:])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokenString, value: value})
			i += length
		case isLetter(char):
			start := i
			for i < len(query) && isLetter(query[i]) {
				i++
			}
			tokens = append(tokens, token{kind: tokenWord, value: query[start:i]})
		default:
			return nil, fmt.Errorf("invalid query %q: unexpected character %q", query, char)
		}
	}
	return tokens, nil
}

func readQuotedString(input string) (string, int, error) {
	var value strings.Builder
	for i := 1; i < len(input); i++ {
		switch input[i] {
		case '\\':
			if i+1 >= len(input) {
				return "", 0, fmt.Errorf("dangling escape in query string")
			}
			i++
			value.WriteByte(input[i])
		case '\'':
			return value.String(), i + 1, nil
		default:
			value.WriteByte(input[i])
		}
	}
	return "", 0, fmt.Errorf("unterminated query string")
}

func isLetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}
