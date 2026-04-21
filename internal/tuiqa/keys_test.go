package tuiqa

import (
	"reflect"
	"testing"
)

func TestTokenToBytes_Ctrl(t *testing.T) {
	got := TokenToBytes("C-c")
	want := []byte{0x03}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TokenToBytes(C-c) = %v, want %v", got, want)
	}
}

func TestParseSendTokens_Quoted(t *testing.T) {
	got := ParseSendTokens(`Tab "hello world"`)
	want := []string{"Tab", "hello world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSendTokens = %#v, want %#v", got, want)
	}
}
