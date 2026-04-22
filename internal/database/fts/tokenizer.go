package fts

import (
	"unicode"

	"github.com/bitmagnet-io/bitmagnet/internal/lexer"
)

func Tokenize(str string) [][]string {
	l := tokenizerLexer{newLexer(str)}

	var tokens [][]string

	for {
		phrase := l.readPhrase()
		if len(phrase) == 0 {
			break
		}

		tokens = append(tokens, phrase)
	}

	return tokens
}

type tokenizerLexer struct {
	ftsLexer
}

func TokenizeFlat(str string) []string {
	var tokens []string
	for _, phrase := range Tokenize(str) {
		tokens = append(tokens, phrase...)
	}

	return tokens
}

func (l *tokenizerLexer) readPhrase() []string {
	var phrase []string

	var lexeme string

	breakWord := func() {
		if lexeme != "" {
			phrase = append(phrase, lexeme)
			lexeme = ""
		}
	}

	for {
		if l.IsEOF() {
			breakWord()
			return phrase
		}

		if ch, ok := l.ReadIf(lexer.IsWordChar); ok {
			ch = unicode.ToLower(ch)
			// If the character is determined to be a language with unspaced words
			// (e.g. Chinese, Japanese), each character will become a token;
			// using this cutoff might not be perfect.
			isNonBreakingLang := ch > '\u1FFF'
			if isNonBreakingLang {
				breakWord()
				phrase = append(phrase, string(ch))
			} else {
				lexeme += string(ch)
			}
			continue
		}

		breakWord()

		if len(phrase) > 0 {
			return phrase
		}

		l.Read()
	}
}
