// Package bigqueryfake is an in-memory BigQuery Storage Read API for tests:
// tables of rows, read sessions that evaluate their row restriction and
// project their selected fields, and Arrow record batches split across
// streams, as the real service returns them.
//
// It understands the row restrictions the audit reader writes and refuses any
// other: a conjunction (AND) of `field = "literal"`, `field IN ("a", "b")` and
// `field op CAST("timestamp" AS TIMESTAMP)` for op one of >=, >, <=, <. A
// restriction it cannot parse fails the session, so a reader change that
// writes something BigQuery might read differently fails its tests loudly
// rather than reading everything.
//
// Rows are the values a streaming insert carries (bigquery.Value maps, as
// bigquerystore.EventRow builds them), so a test fills the fake with exactly
// what the writer sends.
package bigqueryfake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/bigquery/storage/apiv1/storagepb"
	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server is the fake. Its exported fields tune it before the first read.
type Server struct {
	// StreamsPerSession caps how many streams a session's rows are spread over
	// (default 3), further capped by the request's max stream count.
	StreamsPerSession int
	// RowsPerBatch is the rows of one record batch (default 2), so a stream
	// carries several.
	RowsPerBatch int
	// IgnoreRestrictions returns every row of the table, as a store that
	// over-returns would: what the reader answers must not change.
	IgnoreRestrictions bool
	// FailReadRows, when set, is asked before every response of a stream; an
	// error it returns ends the stream with that error.
	FailReadRows func(stream string, offset int64) error

	mu       sync.Mutex
	tables   map[string]*table
	streams  map[string][]*storagepb.ReadRowsResponse
	sessions []*storagepb.CreateReadSessionRequest
	next     int
}

type table struct {
	schema bigquery.Schema
	rows   []map[string]bigquery.Value
}

// New is an empty fake.
func New() *Server {
	return &Server{tables: map[string]*table{}, streams: map[string][]*storagepb.ReadRowsResponse{}}
}

// TablePath names a table as a read session does.
func TablePath(project, dataset, name string) string {
	return fmt.Sprintf("projects/%s/datasets/%s/tables/%s", project, dataset, name)
}

// CreateTable adds an empty table.
func (s *Server) CreateTable(path string, schema bigquery.Schema) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tables[path] = &table{schema: schema}
}

// Insert appends rows to a table, checked against its schema.
func (s *Server) Insert(path string, rows ...map[string]bigquery.Value) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tables[path]
	if !ok {
		return fmt.Errorf("no table %s", path)
	}
	for _, row := range rows {
		typed := make(map[string]bigquery.Value, len(row))
		for _, field := range t.schema {
			value, err := typedValue(field, row[field.Name])
			if err != nil {
				return err
			}
			typed[field.Name] = value
		}
		for name := range row {
			if !slices.ContainsFunc(t.schema, func(f *bigquery.FieldSchema) bool { return f.Name == name }) {
				return fmt.Errorf("table %s has no column %s", path, name)
			}
		}
		t.rows = append(t.rows, typed)
	}
	return nil
}

// Rows is a copy of a table's rows.
func (s *Server) Rows(path string) []map[string]bigquery.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tables[path]
	if !ok {
		return nil
	}
	return slices.Clone(t.rows)
}

// Sessions is every read session request made so far, in order.
func (s *Server) Sessions() []*storagepb.CreateReadSessionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sessions)
}

// typedValue reads an insert value as the column's type: STRING as string,
// INTEGER as int64, BOOLEAN as bool, TIMESTAMP as a UTC time; nil is NULL.
func typedValue(field *bigquery.FieldSchema, value bigquery.Value) (bigquery.Value, error) {
	if value == nil {
		if field.Required {
			return nil, fmt.Errorf("column %s is required", field.Name)
		}
		return nil, nil
	}
	switch field.Type {
	case bigquery.StringFieldType:
		if v, ok := value.(string); ok {
			return v, nil
		}
	case bigquery.IntegerFieldType:
		switch v := value.(type) {
		case int:
			return int64(v), nil
		case int64:
			return v, nil
		}
	case bigquery.BooleanFieldType:
		if v, ok := value.(bool); ok {
			return v, nil
		}
	case bigquery.TimestampFieldType:
		switch v := value.(type) {
		case time.Time:
			return v.UTC().Truncate(time.Microsecond), nil
		case string:
			at, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", field.Name, err)
			}
			return at.UTC(), nil
		}
	}
	return nil, fmt.Errorf("column %s (%s) cannot hold %T", field.Name, field.Type, value)
}

// CreateReadSession evaluates the restriction over the table and spreads the
// selected columns of the admitted rows over the session's streams.
func (s *Server) CreateReadSession(_ context.Context, req *storagepb.CreateReadSessionRequest, _ ...gax.CallOption) (*storagepb.ReadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, req)
	requested := req.GetReadSession()
	t, ok := s.tables[requested.GetTable()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "table %s not found", requested.GetTable())
	}
	if !strings.HasPrefix(requested.GetTable(), req.GetParent()+"/") {
		return nil, status.Errorf(codes.InvalidArgument, "parent %s does not own %s", req.GetParent(), requested.GetTable())
	}
	if requested.GetDataFormat() != storagepb.DataFormat_ARROW {
		return nil, status.Error(codes.InvalidArgument, "the fake serves ARROW only")
	}
	options := requested.GetReadOptions()
	fields, err := selected(t.schema, options.GetSelectedFields())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	where, err := parseRestriction(options.GetRowRestriction())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "row restriction %q: %v", options.GetRowRestriction(), err)
	}
	for _, term := range where {
		if !slices.ContainsFunc(t.schema, func(f *bigquery.FieldSchema) bool { return f.Name == term.field }) {
			return nil, status.Errorf(codes.InvalidArgument, "row restriction names unknown column %s", term.field)
		}
	}
	var admitted []map[string]bigquery.Value
	for _, row := range t.rows {
		if s.IgnoreRestrictions || where.admits(row) {
			admitted = append(admitted, row)
		}
	}

	schema := arrowSchema(fields)
	serializedSchema, err := serializeSchema(schema)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.next++
	name := fmt.Sprintf("projects/fake/locations/us/sessions/s%d", s.next)
	session := &storagepb.ReadSession{
		Name:       name,
		Table:      requested.GetTable(),
		DataFormat: storagepb.DataFormat_ARROW,
		Schema:     &storagepb.ReadSession_ArrowSchema{ArrowSchema: &storagepb.ArrowSchema{SerializedSchema: serializedSchema}},
	}
	if len(admitted) == 0 {
		return session, nil
	}
	streams := s.StreamsPerSession
	if streams <= 0 {
		streams = 3
	}
	if limit := int(req.GetMaxStreamCount()); limit > 0 && streams > limit {
		streams = limit
	}
	streams = min(streams, len(admitted))
	perBatch := s.RowsPerBatch
	if perBatch <= 0 {
		perBatch = 2
	}
	for i := 0; i < streams; i++ {
		var share []map[string]bigquery.Value
		for j := i; j < len(admitted); j += streams {
			share = append(share, admitted[j])
		}
		streamName := fmt.Sprintf("%s/streams/%d", name, i)
		var responses []*storagepb.ReadRowsResponse
		for start := 0; start < len(share); start += perBatch {
			rows := share[start:min(start+perBatch, len(share))]
			batch, err := serializeBatch(schema, len(serializedSchema), fields, rows)
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			responses = append(responses, &storagepb.ReadRowsResponse{
				RowCount: int64(len(rows)),
				Rows:     &storagepb.ReadRowsResponse_ArrowRecordBatch{ArrowRecordBatch: &storagepb.ArrowRecordBatch{SerializedRecordBatch: batch}},
			})
		}
		s.streams[streamName] = responses
		session.Streams = append(session.Streams, &storagepb.ReadStream{Name: streamName})
	}
	return session, nil
}

// ReadRows serves a stream from the row offset the request names, which must
// fall on a batch boundary, as every offset the reader resumes from does.
func (s *Server) ReadRows(ctx context.Context, req *storagepb.ReadRowsRequest, _ ...gax.CallOption) (storagepb.BigQueryRead_ReadRowsClient, error) {
	s.mu.Lock()
	responses, ok := s.streams[req.GetReadStream()]
	fail := s.FailReadRows
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "stream %s not found", req.GetReadStream())
	}
	var skipped int64
	start := 0
	for start < len(responses) && skipped < req.GetOffset() {
		skipped += responses[start].GetRowCount()
		start++
	}
	if skipped != req.GetOffset() {
		return nil, status.Errorf(codes.OutOfRange, "offset %d is not a batch boundary", req.GetOffset())
	}
	return &rowStream{ctx: ctx, stream: req.GetReadStream(), responses: responses[start:], offset: skipped, fail: fail}, nil
}

type rowStream struct {
	grpc.ClientStream
	ctx       context.Context
	stream    string
	responses []*storagepb.ReadRowsResponse
	offset    int64
	fail      func(string, int64) error
}

func (r *rowStream) Recv() (*storagepb.ReadRowsResponse, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if len(r.responses) == 0 {
		return nil, io.EOF
	}
	if r.fail != nil {
		if err := r.fail(r.stream, r.offset); err != nil {
			return nil, err
		}
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	r.offset += response.GetRowCount()
	return response, nil
}

func (r *rowStream) Context() context.Context { return r.ctx }

// selected is the selected fields in table order, as BigQuery orders them;
// none selected is every field.
func selected(schema bigquery.Schema, names []string) (bigquery.Schema, error) {
	if len(names) == 0 {
		return schema, nil
	}
	for _, name := range names {
		if !slices.ContainsFunc(schema, func(f *bigquery.FieldSchema) bool { return f.Name == name }) {
			return nil, fmt.Errorf("selected field %s is not in the table", name)
		}
	}
	var out bigquery.Schema
	for _, field := range schema {
		if slices.Contains(names, field.Name) {
			out = append(out, field)
		}
	}
	return out, nil
}

func arrowSchema(fields bigquery.Schema) *arrow.Schema {
	out := make([]arrow.Field, len(fields))
	for i, field := range fields {
		var dataType arrow.DataType
		switch field.Type {
		case bigquery.IntegerFieldType:
			dataType = arrow.PrimitiveTypes.Int64
		case bigquery.BooleanFieldType:
			dataType = arrow.FixedWidthTypes.Boolean
		case bigquery.TimestampFieldType:
			dataType = &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
		default:
			dataType = arrow.BinaryTypes.String
		}
		out[i] = arrow.Field{Name: field.Name, Type: dataType, Nullable: !field.Required}
	}
	return arrow.NewSchema(out, nil)
}

// serializeSchema is the IPC schema message alone, as a read session carries
// it. A stream writer emits the schema before its first batch; writing one
// batch twice tells the two apart, since both copies of the batch are the same
// length.
func serializeSchema(schema *arrow.Schema) ([]byte, error) {
	record := array.NewRecord(schema, emptyColumns(schema), 0)
	defer record.Release()
	var buf bytes.Buffer
	writer := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	if err := writer.Write(record); err != nil {
		return nil, err
	}
	once := buf.Len()
	if err := writer.Write(record); err != nil {
		return nil, err
	}
	batchLength := buf.Len() - once
	return slices.Clone(buf.Bytes()[:once-batchLength]), nil
}

func emptyColumns(schema *arrow.Schema) []arrow.Array {
	columns := make([]arrow.Array, schema.NumFields())
	for i, field := range schema.Fields() {
		builder := array.NewBuilder(memory.DefaultAllocator, field.Type)
		columns[i] = builder.NewArray()
		builder.Release()
	}
	return columns
}

// serializeBatch is one IPC record batch message, without the schema that
// precedes it in a stream.
func serializeBatch(schema *arrow.Schema, schemaLength int, fields bigquery.Schema, rows []map[string]bigquery.Value) ([]byte, error) {
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for _, row := range rows {
		for i, field := range fields {
			value := row[field.Name]
			column := builder.Field(i)
			if value == nil {
				column.AppendNull()
				continue
			}
			switch b := column.(type) {
			case *array.StringBuilder:
				b.Append(value.(string))
			case *array.Int64Builder:
				b.Append(value.(int64))
			case *array.BooleanBuilder:
				b.Append(value.(bool))
			case *array.TimestampBuilder:
				b.Append(arrow.Timestamp(value.(time.Time).UnixMicro()))
			default:
				return nil, fmt.Errorf("no builder for column %s", field.Name)
			}
		}
	}
	record := builder.NewRecord()
	defer record.Release()
	var buf bytes.Buffer
	writer := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	if err := writer.Write(record); err != nil {
		return nil, err
	}
	if buf.Len() < schemaLength {
		return nil, errors.New("record batch shorter than its schema")
	}
	return slices.Clone(buf.Bytes()[schemaLength:]), nil
}

// term is one comparison of a restriction.
type term struct {
	field  string
	op     string
	values []any // string or time.Time
}

type conjunction []term

func (c conjunction) admits(row map[string]bigquery.Value) bool {
	for _, t := range c {
		if !t.admits(row[t.field]) {
			return false
		}
	}
	return true
}

func (t term) admits(value bigquery.Value) bool {
	if value == nil {
		return false
	}
	switch t.op {
	case "IN":
		for _, candidate := range t.values {
			if equal(value, candidate) {
				return true
			}
		}
		return false
	case "=":
		return equal(value, t.values[0])
	}
	at, ok := value.(time.Time)
	bound, boundOK := t.values[0].(time.Time)
	if !ok || !boundOK {
		return false
	}
	switch t.op {
	case ">=":
		return !at.Before(bound)
	case ">":
		return at.After(bound)
	case "<=":
		return !at.After(bound)
	case "<":
		return at.Before(bound)
	}
	return false
}

func equal(value bigquery.Value, literal any) bool {
	switch v := value.(type) {
	case string:
		s, ok := literal.(string)
		return ok && v == s
	case time.Time:
		at, ok := literal.(time.Time)
		return ok && v.Equal(at)
	}
	return false
}

// parseRestriction parses the restriction grammar the package comment names.
func parseRestriction(text string) (conjunction, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	tokens, err := tokenize(text)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	var out conjunction
	for {
		t, err := p.term()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		if p.done() {
			return out, nil
		}
		if err := p.expect("AND"); err != nil {
			return nil, err
		}
	}
}

type token struct {
	kind string // ident, op, string, punct
	text string
}

func tokenize(text string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ':
			i++
		case c == '(' || c == ')' || c == ',':
			tokens = append(tokens, token{kind: "punct", text: string(c)})
			i++
		case c == '=':
			tokens = append(tokens, token{kind: "op", text: "="})
			i++
		case c == '<' || c == '>':
			op := string(c)
			if i+1 < len(text) && text[i+1] == '=' {
				op += "="
			}
			tokens = append(tokens, token{kind: "op", text: op})
			i += len(op)
		case c == '"':
			value, n, err := unquote(text[i:])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: "string", text: value})
			i += n
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(text) && (text[j] == '_' || text[j] >= 'a' && text[j] <= 'z' || text[j] >= 'A' && text[j] <= 'Z' || text[j] >= '0' && text[j] <= '9') {
				j++
			}
			tokens = append(tokens, token{kind: "ident", text: text[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q at %d", c, i)
		}
	}
	return tokens, nil
}

// unquote reads a double-quoted GoogleSQL string literal with the escapes the
// reader writes: \" \\ and \Uhhhhhhhh.
func unquote(text string) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(text); i++ {
		switch c := text[i]; c {
		case '"':
			return b.String(), i + 1, nil
		case '\\':
			if i+1 >= len(text) {
				return "", 0, errors.New("unterminated escape")
			}
			switch text[i+1] {
			case '"', '\\':
				b.WriteByte(text[i+1])
				i++
			case 'U':
				if i+10 > len(text) {
					return "", 0, errors.New("short \\U escape")
				}
				var r rune
				if _, err := fmt.Sscanf(text[i+2:i+10], "%08X", &r); err != nil {
					return "", 0, fmt.Errorf("bad \\U escape: %w", err)
				}
				b.WriteRune(r)
				i += 9
			default:
				return "", 0, fmt.Errorf("escape \\%c is not one the reader writes", text[i+1])
			}
		default:
			if c < 0x20 || c >= 0x7f {
				return "", 0, fmt.Errorf("raw byte %#x in a literal", c)
			}
			b.WriteByte(c)
		}
	}
	return "", 0, errors.New("unterminated string literal")
}

type parser struct {
	tokens []token
	at     int
}

func (p *parser) done() bool { return p.at == len(p.tokens) }

func (p *parser) peek() token {
	if p.done() {
		return token{}
	}
	return p.tokens[p.at]
}

func (p *parser) take() token {
	t := p.peek()
	p.at++
	return t
}

func (p *parser) expect(text string) error {
	if t := p.take(); t.text != text || t.kind == "string" {
		return fmt.Errorf("expected %s, got %q", text, t.text)
	}
	return nil
}

func (p *parser) term() (term, error) {
	field := p.take()
	if field.kind != "ident" {
		return term{}, fmt.Errorf("expected a column, got %q", field.text)
	}
	op := p.take()
	switch {
	case op.kind == "ident" && op.text == "IN":
		if err := p.expect("("); err != nil {
			return term{}, err
		}
		t := term{field: field.text, op: "IN"}
		for {
			value := p.take()
			if value.kind != "string" {
				return term{}, fmt.Errorf("IN takes string literals, got %q", value.text)
			}
			t.values = append(t.values, value.text)
			next := p.take()
			if next.text == ")" {
				return t, nil
			}
			if next.text != "," {
				return term{}, fmt.Errorf("expected , or ), got %q", next.text)
			}
		}
	case op.kind == "op":
		value, err := p.value()
		if err != nil {
			return term{}, err
		}
		if _, isTime := value.(time.Time); op.text != "=" && !isTime {
			return term{}, fmt.Errorf("%s compares timestamps only", op.text)
		}
		return term{field: field.text, op: op.text, values: []any{value}}, nil
	default:
		return term{}, fmt.Errorf("expected an operator, got %q", op.text)
	}
}

func (p *parser) value() (any, error) {
	t := p.take()
	if t.kind == "string" {
		return t.text, nil
	}
	if t.kind != "ident" || t.text != "CAST" {
		return nil, fmt.Errorf("expected a literal, got %q", t.text)
	}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	literal := p.take()
	if literal.kind != "string" {
		return nil, fmt.Errorf("CAST takes a string literal, got %q", literal.text)
	}
	if err := p.expect("AS"); err != nil {
		return nil, err
	}
	if err := p.expect("TIMESTAMP"); err != nil {
		return nil, err
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	at, err := time.Parse("2006-01-02 15:04:05.000000-07:00", literal.text)
	if err != nil {
		return nil, fmt.Errorf("timestamp literal: %w", err)
	}
	return at.UTC(), nil
}
