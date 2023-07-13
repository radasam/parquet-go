//go:build go1.18

package parquet_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"testing"

	"github.com/parquet-go/parquet-go"
	"google.golang.org/protobuf/types/known/structpb"
)

func ExampleReadFile() {
	type Row struct {
		ID   int64  `parquet:"id"`
		Name string `parquet:"name,zstd"`
	}

	ExampleWriteFile()

	rows, err := parquet.ReadFile[Row]("/tmp/file.parquet")
	if err != nil {
		log.Fatal(err)
	}

	for _, row := range rows {
		fmt.Printf("%d: %q\n", row.ID, row.Name)
	}

	// Output:
	// 0: "Bob"
	// 1: "Alice"
	// 2: "Franky"
}

func ExampleWriteFile() {
	type Row struct {
		ID   int64  `parquet:"id"`
		Name string `parquet:"name,zstd"`
	}

	if err := parquet.WriteFile("/tmp/file.parquet", []Row{
		{ID: 0, Name: "Bob"},
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Franky"},
	}); err != nil {
		log.Fatal(err)
	}

	// Output:
}

func ExampleRead_any() {
	type Row struct{ FirstName, LastName string }

	buf := new(bytes.Buffer)
	err := parquet.Write(buf, []Row{
		{FirstName: "Luke", LastName: "Skywalker"},
		{FirstName: "Han", LastName: "Solo"},
		{FirstName: "R2", LastName: "D2"},
	})
	if err != nil {
		log.Fatal(err)
	}

	file := bytes.NewReader(buf.Bytes())

	rows, err := parquet.Read[any](file, file.Size())
	if err != nil {
		log.Fatal(err)
	}

	for _, row := range rows {
		fmt.Printf("%q\n", row)
	}

	// Output:
	// map["FirstName":"Luke" "LastName":"Skywalker"]
	// map["FirstName":"Han" "LastName":"Solo"]
	// map["FirstName":"R2" "LastName":"D2"]
}

func ExampleWrite_any() {
	schema := parquet.SchemaOf(struct {
		FirstName string
		LastName  string
	}{})

	buf := new(bytes.Buffer)
	err := parquet.Write[any](
		buf,
		[]any{
			map[string]string{"FirstName": "Luke", "LastName": "Skywalker"},
			map[string]string{"FirstName": "Han", "LastName": "Solo"},
			map[string]string{"FirstName": "R2", "LastName": "D2"},
		},
		schema,
	)
	if err != nil {
		log.Fatal(err)
	}

	file := bytes.NewReader(buf.Bytes())

	rows, err := parquet.Read[any](file, file.Size())
	if err != nil {
		log.Fatal(err)
	}

	for _, row := range rows {
		fmt.Printf("%q\n", row)
	}

	// Output:
	// map["FirstName":"Luke" "LastName":"Skywalker"]
	// map["FirstName":"Han" "LastName":"Solo"]
	// map["FirstName":"R2" "LastName":"D2"]
}

func ExampleSearch() {
	type Row struct{ FirstName, LastName string }

	buf := new(bytes.Buffer)
	// The column being searched should be sorted to avoid a full scan of the
	// column. See the section of the readme on sorting for how to sort on
	// insertion into the parquet file using parquet.SortingColumns
	rows := []Row{
		{FirstName: "C", LastName: "3PO"},
		{FirstName: "Han", LastName: "Solo"},
		{FirstName: "Leia", LastName: "Organa"},
		{FirstName: "Luke", LastName: "Skywalker"},
		{FirstName: "R2", LastName: "D2"},
	}
	// The tiny page buffer size ensures we get multiple pages out of the example above.
	w := parquet.NewGenericWriter[Row](buf, parquet.PageBufferSize(12), parquet.WriteBufferSize(0))
	// Need to write 1 row at a time here as writing many at once disregards PageBufferSize option.
	for _, row := range rows {
		_, err := w.Write([]Row{row})
		if err != nil {
			log.Fatal(err)
		}
	}
	err := w.Close()
	if err != nil {
		log.Fatal(err)
	}

	reader := bytes.NewReader(buf.Bytes())
	file, err := parquet.OpenFile(reader, reader.Size())
	if err != nil {
		log.Fatal(err)
	}

	// Search is scoped to a single RowGroup/ColumnChunk
	rowGroup := file.RowGroups()[0]
	firstNameColChunk := rowGroup.ColumnChunks()[0]

	found := parquet.Search(firstNameColChunk.ColumnIndex(), parquet.ValueOf("Luke"), parquet.ByteArrayType)
	offsetIndex := firstNameColChunk.OffsetIndex()
	fmt.Printf("numPages: %d\n", offsetIndex.NumPages())
	fmt.Printf("result found in page: %d\n", found)
	if found < offsetIndex.NumPages() {
		r := parquet.NewGenericReader[Row](file)
		defer r.Close()
		// Seek to the first row in the page the result was found
		r.SeekToRow(offsetIndex.FirstRowIndex(found))
		result := make([]Row, 2)
		_, _ = r.Read(result)
		// Leia is in index 0 for the page.
		for _, row := range result {
			if row.FirstName == "Luke" {
				fmt.Printf("%q\n", row)
			}
		}
	}

	// Output:
	// numPages: 3
	// result found in page: 1
	// {"Luke" "Skywalker"}
}

func TestIssue360(t *testing.T) {
	type TestType struct {
		Key []int
	}

	schema := parquet.SchemaOf(TestType{})
	buffer := parquet.NewGenericBuffer[any](schema)

	data := make([]any, 1)
	data[0] = TestType{Key: []int{1}}
	_, err := buffer.Write(data)
	if err != nil {
		fmt.Println("Exiting with error: ", err)
		return
	}

	var out bytes.Buffer
	writer := parquet.NewGenericWriter[any](&out, schema)

	_, err = parquet.CopyRows(writer, buffer.Rows())
	if err != nil {
		fmt.Println("Exiting with error: ", err)
		return
	}
	writer.Close()

	br := bytes.NewReader(out.Bytes())
	rows, _ := parquet.Read[any](br, br.Size())

	expect := []any{
		map[string]any{
			"Key": []any{
				int64(1),
			},
		},
	}

	assertRowsEqual(t, expect, rows)
}

func TestIssue362ParquetReadFromGenericReaders(t *testing.T) {
	path := "testdata/dms_test_table_LOAD00000001.parquet"
	fp, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	r1 := parquet.NewGenericReader[any](fp)
	rows1 := make([]any, r1.NumRows())
	_, err = r1.Read(rows1)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}

	r2 := parquet.NewGenericReader[any](fp)
	rows2 := make([]any, r2.NumRows())
	_, err = r2.Read(rows2)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
}

func TestIssue362ParquetReadFile(t *testing.T) {
	rows1, err := parquet.ReadFile[any]("testdata/dms_test_table_LOAD00000001.parquet")
	if err != nil {
		t.Fatal(err)
	}

	rows2, err := parquet.ReadFile[any]("testdata/dms_test_table_LOAD00000001.parquet")
	if err != nil {
		t.Fatal(err)
	}

	assertRowsEqual(t, rows1, rows2)
}

func TestIssue368(t *testing.T) {
	f, err := os.Open("testdata/issue368.parquet")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}

	reader := parquet.NewGenericReader[any](pf)
	defer reader.Close()

	trs := make([]any, 1)
	for {
		_, err := reader.Read(trs)
		if err != nil {
			break
		}
	}
}

func TestIssue377(t *testing.T) {
	type People struct {
		Name string
		Age  int
	}

	type Nested struct {
		P  []People
		F  string
		GF string
	}
	row1 := Nested{P: []People{
		{
			Name: "Bob",
			Age:  10,
		}}}
	ods := []Nested{
		row1,
	}
	buf := new(bytes.Buffer)
	w := parquet.NewGenericWriter[Nested](buf)
	_, err := w.Write(ods)
	if err != nil {
		t.Fatal("write error: ", err)
	}
	w.Close()

	file := bytes.NewReader(buf.Bytes())
	rows, err := parquet.Read[Nested](file, file.Size())
	if err != nil {
		t.Fatal("read error: ", err)
	}

	assertRowsEqual(t, rows, ods)
}

func TestIssue423(t *testing.T) {
	type Inner struct {
		Value string `parquet:","`
	}
	type Outer struct {
		Label string  `parquet:","`
		Inner Inner   `parquet:",json"`
		Slice []Inner `parquet:",json"`
		// This is the only tricky situation. Because we're delegating to json Marshaler/Unmarshaler
		// We use the json tags for optionality.
		Ptr *Inner `json:",omitempty" parquet:",json"`

		// This tests BC behavior that slices of bytes and json strings still get written/read in a BC way.
		String        string                     `parquet:",json"`
		Bytes         []byte                     `parquet:",json"`
		MapOfStructPb map[string]*structpb.Value `parquet:",json"`
		StructPB      *structpb.Value            `parquet:",json"`
	}

	writeRows := []Outer{
		{
			Label: "welp",
			Inner: Inner{
				Value: "this is a string",
			},
			Slice: []Inner{
				{
					Value: "in a slice",
				},
			},
			Ptr:    nil,
			String: `{"hello":"world"}`,
			Bytes:  []byte(`{"goodbye":"world"}`),
			MapOfStructPb: map[string]*structpb.Value{
				"answer": structpb.NewNumberValue(42.00),
			},
			StructPB: structpb.NewBoolValue(true),
		},
		{
			Label: "foxes",
			Inner: Inner{
				Value: "the quick brown fox jumped over the yellow lazy dog.",
			},
			Slice: []Inner{
				{
					Value: "in a slice",
				},
			},
			Ptr: &Inner{
				Value: "not nil",
			},
			String: `{"hello":"world"}`,
			Bytes:  []byte(`{"goodbye":"world"}`),
			MapOfStructPb: map[string]*structpb.Value{
				"doubleAnswer": structpb.NewNumberValue(84.00),
			},
			StructPB: structpb.NewBoolValue(false),
		},
	}

	schema := parquet.SchemaOf(new(Outer))
	fmt.Println(schema.String())
	buf := new(bytes.Buffer)
	w := parquet.NewGenericWriter[Outer](buf, schema)
	_, err := w.Write(writeRows)
	if err != nil {
		t.Fatal("write error: ", err)
	}
	w.Close()

	file := bytes.NewReader(buf.Bytes())
	readRows, err := parquet.Read[Outer](file, file.Size())
	if err != nil {
		t.Fatal("read error: ", err)
	}

	assertRowsEqual(t, writeRows, readRows)
}

func TestReadFileGenericMultipleRowGroupsMultiplePages(t *testing.T) {
	type MyRow struct {
		ID    [16]byte `parquet:"id,delta,uuid"`
		File  string   `parquet:"file,dict,zstd"`
		Index int64    `parquet:"index,delta,zstd"`
	}

	numRows := 20_000
	maxPageBytes := 5000

	tmp, err := os.CreateTemp("/tmp", "*.parquet")
	if err != nil {
		t.Fatal("os.CreateTemp: ", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	t.Log("file:", path)

	// The page buffer size ensures we get multiple pages out of this example.
	w := parquet.NewGenericWriter[MyRow](tmp, parquet.PageBufferSize(maxPageBytes))
	// Need to write 1 row at a time here as writing many at once disregards PageBufferSize option.
	for i := 0; i < numRows; i++ {
		row := MyRow{
			ID:    [16]byte{15: byte(i)},
			File:  "hi" + fmt.Sprint(i),
			Index: int64(i),
		}
		_, err := w.Write([]MyRow{row})
		if err != nil {
			t.Fatal("w.Write: ", err)
		}
		// Flush writes rows as row group. 4 total (20k/5k) in this file.
		if (i+1)%maxPageBytes == 0 {
			err = w.Flush()
			if err != nil {
				t.Fatal("w.Flush: ", err)
			}
		}
	}
	err = w.Close()
	if err != nil {
		t.Fatal("w.Close: ", err)
	}
	err = tmp.Close()
	if err != nil {
		t.Fatal("tmp.Close: ", err)
	}

	rows, err := parquet.ReadFile[MyRow](path)
	if err != nil {
		t.Fatal("parquet.ReadFile: ", err)
	}

	if len(rows) != numRows {
		t.Fatalf("not enough values were read: want=%d got=%d", len(rows), numRows)
	}
	for i, row := range rows {
		id := [16]byte{15: byte(i)}
		file := "hi" + fmt.Sprint(i)
		index := int64(i)

		if row.ID != id || row.File != file || row.Index != index {
			t.Fatalf("rows mismatch at index: %d got: %+v", i, row)
		}
	}
}

func assertRowsEqual[T any](t *testing.T, rows1, rows2 []T) {
	if !reflect.DeepEqual(rows1, rows2) {
		t.Error("rows mismatch")

		t.Log("want:")
		logRows(t, rows1)

		t.Log("got:")
		logRows(t, rows2)
	}
}

func logRows[T any](t *testing.T, rows []T) {
	for _, row := range rows {
		t.Logf(". %#v\n", row)
	}
}

func TestNestedPointer(t *testing.T) {
	type InnerStruct struct {
		InnerField string
	}

	type SliceElement struct {
		Inner *InnerStruct
	}

	type Outer struct {
		Slice []*SliceElement
	}
	value := "inner-string"
	in := &Outer{
		Slice: []*SliceElement{
			{
				Inner: &InnerStruct{
					InnerField: value,
				},
			},
		},
	}

	var f bytes.Buffer

	pw := parquet.NewGenericWriter[*Outer](&f)
	_, err := pw.Write([]*Outer{in})
	if err != nil {
		t.Fatal(err)
	}

	err = pw.Close()
	if err != nil {
		t.Fatal(err)
	}

	pr := parquet.NewGenericReader[*Outer](bytes.NewReader(f.Bytes()))

	out := make([]*Outer, 1)
	_, err = pr.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	pr.Close()
	if want, got := value, out[0].Slice[0].Inner.InnerField; want != got {
		t.Error("failed to set inner field pointer")
	}
}
