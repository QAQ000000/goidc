package csvexport

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestEncodeSpreadsheetTextAndExactIntegers(t *testing.T) {
	inputs := []any{"=1+1", "+1+1", "-1+1", "@SUM(1)", "  =1", " \t+1", "\ttext", "\rtext", "\ntext", " \r\n@SUM(1)", "", "客户甲", "00123", "a=b", "'quoted", "two,columns", nil, json.Number("9007199254740993"), json.Number("-9007199254740993"), int64(-42), int64(math.MaxInt64), uint64(math.MaxUint64)}
	want := []string{"'=1+1", "'+1+1", "'-1+1", "'@SUM(1)", "'  =1", "' \t+1", "'\ttext", "'\rtext", "'\ntext", "' \n@SUM(1)", "", "客户甲", "00123", "a=b", "'quoted", "two,columns", "", "9007199254740993", "-9007199254740993", "-42", "9223372036854775807", "18446744073709551615"}
	rows := make([][]any, len(inputs))
	for i, input := range inputs {
		rows[i] = []any{input, "row"}
	}
	content, err := Encode([]string{"值", "标记"}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(content, []byte("\xef\xbb\xbf")) || !utf8.Valid(content) {
		t.Fatal("missing UTF-8 BOM or invalid UTF-8")
	}
	reader := csv.NewReader(bytes.NewReader(content[3:]))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(want)+1 || records[0][0] != "值" {
		t.Fatalf("records=%#v", records)
	}
	for i, value := range want {
		if records[i+1][0] != value {
			t.Errorf("input=%#v got=%q want=%q", inputs[i], records[i+1][0], value)
		}
	}
}

func TestEncodeCSVQuotingAndEmptyRows(t *testing.T) {
	content, err := Encode([]string{"名称", "说明"}, [][]any{{"测试,\"客户\"\n第二行", "中文"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "\xef\xbb\xbf名称,说明\n\"测试,\"\"客户\"\"\n第二行\",中文\n"
	if string(content) != want {
		t.Fatalf("CSV quoting changed: got=%q want=%q", content, want)
	}
	content, err = Encode([]string{"名称", "金额（分）"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(content[3:])).ReadAll()
	if err != nil || !reflect.DeepEqual(records, [][]string{{"名称", "金额（分）"}}) {
		t.Fatalf("empty export records=%#v err=%v", records, err)
	}
}
