package errors

import (
	"fmt"
	"io"
	"strings"

	"github.com/pkg/errors"
)

type Code int

const (
	internal Code = iota
	notFound
	alreadyExists
	unauthorized
	forbidden
	badRequest
)

type Error struct {
	error
	key string

	code Code
}

func New(text string) error {
	return &Error{
		error: errors.New(text),
	}
}

func As(err error, target any) bool {
	return errors.As(err, target)
}

func Is(err error, target error) bool {
	err1 := new(Error)
	err2 := new(Error)
	if errors.As(err, &err1) && errors.As(target, &err2) {
		return err1.key == err2.key
	}

	return errors.Is(err, target)
}

func WrapCK(err error, code Code, key string) error {
	if Is(err, new(Error)) {
		return err
	}

	return &Error{
		error: New(err.Error()),
		code:  code,
		key:   makeErrorKey(key),
	}
}

func Wrap(err error) error {
	if Is(err, new(Error)) {
		return err
	}

	return &Error{
		error: New(err.Error()),
		key:   makeErrorKey("internal"),
		code:  internal,
	}
}

func (e *Error) Format(s fmt.State, verb rune) {
	if formatter, ok := e.error.(fmt.Formatter); ok {
		formatter.Format(s, verb)

		return
	}

	_, err := io.WriteString(s, e.Error())
	if err != nil {
		panic(err)
	}
}

func (e *Error) Unwrap() error {
	return e.error
}

func makeErrorKey(msg string) string {
	return strings.ToUpper(strings.ReplaceAll(msg, " ", "_"))
}
