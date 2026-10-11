package importexport

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Dataset is a table of text cells, the exchange format between resources and file formats
// (tablib's Dataset in django-import-export).
type Dataset struct {
	Headers []string
	Rows    [][]string
}

// NewDataset returns an empty dataset with the given headers.
func NewDataset(headers ...string) *Dataset { return &Dataset{Headers: headers} }

// Append adds a row of cells.
func (d *Dataset) Append(cells ...string) { d.Rows = append(d.Rows, cells) }

// Row returns the row at index keyed by header. Missing trailing cells are empty.
func (d *Dataset) Row(index int) Row {
	row := make(Row, len(d.Headers))
	cells := d.Rows[index]
	for position, header := range d.Headers {
		if position < len(cells) {
			row[header] = cells[position]
		} else {
			row[header] = ""
		}
	}
	return row
}

// Row is one dataset row keyed by header.
type Row map[string]string

// Format reads and writes datasets in a file format.
type Format interface {
	// Name is the short name and file extension, e.g. "csv".
	Name() string
	ContentType() string
	Write(writer io.Writer, dataset *Dataset) error
	Read(reader io.Reader) (*Dataset, error)
}

var (
	// CSV is comma separated values with a header row.
	CSV Format = delimitedFormat{name: "csv", contentType: "text/csv", delimiter: ','}
	// TSV is tab separated values with a header row.
	TSV Format = delimitedFormat{name: "tsv", contentType: "text/tab-separated-values", delimiter: '\t'}
	// JSON is an array of objects with string values; keys keep the header order.
	JSON Format = jsonFormat{}
)

type delimitedFormat struct {
	name, contentType string
	delimiter         rune
}

func (f delimitedFormat) Name() string        { return f.name }
func (f delimitedFormat) ContentType() string { return f.contentType }

func (f delimitedFormat) Write(writer io.Writer, dataset *Dataset) error {
	csvWriter := csv.NewWriter(writer)
	csvWriter.Comma = f.delimiter
	if err := csvWriter.Write(dataset.Headers); err != nil {
		return err
	}
	if err := csvWriter.WriteAll(dataset.Rows); err != nil {
		return err
	}
	return csvWriter.Error()
}

func (f delimitedFormat) Read(reader io.Reader) (*Dataset, error) {
	buffered := bufio.NewReader(reader)
	if bom, err := buffered.Peek(3); err == nil && bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
		_, _ = buffered.Discard(3)
	}
	csvReader := csv.NewReader(buffered)
	csvReader.Comma = f.delimiter
	records, err := csvReader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("importexport: reading %s: %w", f.name, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("importexport: reading %s: the file is empty", f.name)
	}
	headers := records[0]
	for index := range headers {
		headers[index] = strings.TrimSpace(headers[index])
	}
	return &Dataset{Headers: headers, Rows: records[1:]}, nil
}

type jsonFormat struct{}

func (jsonFormat) Name() string        { return "json" }
func (jsonFormat) ContentType() string { return "application/json" }

func (jsonFormat) Write(writer io.Writer, dataset *Dataset) error {
	var buffer bytes.Buffer
	buffer.WriteString("[")
	for rowIndex := range dataset.Rows {
		if rowIndex > 0 {
			buffer.WriteString(",")
		}
		buffer.WriteString("\n  {")
		row := dataset.Row(rowIndex)
		for headerIndex, header := range dataset.Headers {
			if headerIndex > 0 {
				buffer.WriteString(", ")
			}
			buffer.WriteString(strconv.Quote(header) + ": ")
			encoded, err := json.Marshal(row[header])
			if err != nil {
				return err
			}
			buffer.Write(encoded)
		}
		buffer.WriteString("}")
	}
	if len(dataset.Rows) > 0 {
		buffer.WriteString("\n")
	}
	buffer.WriteString("]\n")
	_, err := writer.Write(buffer.Bytes())
	return err
}

func (jsonFormat) Read(reader io.Reader) (*Dataset, error) {
	var objects []json.RawMessage
	if err := json.NewDecoder(reader).Decode(&objects); err != nil {
		return nil, fmt.Errorf("importexport: reading json: expected an array of objects: %w", err)
	}
	dataset := &Dataset{}
	positions := map[string]int{}
	var rows []Row
	for index, raw := range objects {
		row, keys, err := decodeObject(raw)
		if err != nil {
			return nil, fmt.Errorf("importexport: reading json: item %d: %w", index+1, err)
		}
		for _, key := range keys {
			if _, seen := positions[key]; !seen {
				positions[key] = len(dataset.Headers)
				dataset.Headers = append(dataset.Headers, key)
			}
		}
		rows = append(rows, row)
	}
	for _, row := range rows {
		cells := make([]string, len(dataset.Headers))
		for key, value := range row {
			cells[positions[key]] = value
		}
		dataset.Rows = append(dataset.Rows, cells)
	}
	return dataset, nil
}

// decodeObject reads a JSON object keeping its key order. Scalars become their text, null
// becomes empty and nested arrays or objects keep their JSON text.
func decodeObject(raw json.RawMessage) (Row, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, nil, errors.New("expected an object")
	}
	row := Row{}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		var text string
		switch {
		case string(value) == "null":
		case value[0] == '"':
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, nil, err
			}
		default:
			text = string(value)
		}
		if _, seen := row[key]; !seen {
			keys = append(keys, key)
		}
		row[key] = text
	}
	return row, keys, nil
}
