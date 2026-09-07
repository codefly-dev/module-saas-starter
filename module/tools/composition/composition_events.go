package composition

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
	protoparser "github.com/yoheimuta/go-protoparser/v4"
	"github.com/yoheimuta/go-protoparser/v4/parser"
)

const (
	eventsContributionSchema = "codefly/saas/events-contribution/v1"
	eventsCatalogSchema      = "codefly/saas/events-catalog/v1"
)

var (
	eventTypePattern    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$`)
	eventQueuePattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	versionDirPattern   = regexp.MustCompile(`^v([0-9]+)$`)
	eventVisibilitySet  = map[string]struct{}{"internal": {}, "tenant": {}, "external": {}}
	eventDeliverySet    = map[string]struct{}{"ordered": {}, "unordered": {}}
)

// EventsContribution is one module's declaration of the domain events it
// publishes and consumes, a sibling of PermissionsContribution. It is discovered
// and merged into the base-manifest-tracked event catalog exactly like a
// permissions contribution.
type EventsContribution struct {
	Schema    string           `yaml:"schema"`
	Namespace string           `yaml:"namespace"`
	Queues    []string         `yaml:"queues"`
	Publishes []PublishedEvent `yaml:"publishes"`
	Consumes  []ConsumedEvent  `yaml:"consumes"`
	Owner     string           `yaml:"-"`
}

type PublishedEvent struct {
	Type       string `yaml:"type"`
	Schema     string `yaml:"schema"`
	Visibility string `yaml:"visibility"`
	Partition  string `yaml:"partition"`
	Retention  string `yaml:"retention"`
}

type ConsumedEvent struct {
	Type     string `yaml:"type"`
	Queue    string `yaml:"queue"`
	Delivery string `yaml:"delivery"`
}

type eventCatalog struct {
	Schema    string                `json:"schema"`
	Publishes []eventCatalogPublish `json:"publishes"`
	Consumes  []eventCatalogConsume `json:"consumes"`
}

type eventCatalogPublish struct {
	Type       string       `json:"type"`
	Namespace  string       `json:"namespace"`
	Schema     string       `json:"schema"`
	Major      int          `json:"major"`
	Visibility string       `json:"visibility"`
	Partition  string       `json:"partition,omitempty"`
	Retention  string       `json:"retention,omitempty"`
	Fields     []eventField `json:"fields"`
}

type eventField struct {
	Name   string `json:"name"`
	Number int    `json:"number"`
	Type   string `json:"type"`
}

type eventCatalogConsume struct {
	Type       string `json:"type"`
	Subscriber string `json:"subscriber"`
	Queue      string `json:"queue"`
	Delivery   string `json:"delivery"`
}

// buildEventCatalog validates every events contribution and merges them into the
// deterministic catalog. It fails compose on namespace-ownership, unresolved
// schema, duplicate type, unregistered or undeclared-queue consumes, and any
// breaking field change against the previously generated catalog.
func buildEventCatalog(contributions []EventsContribution, manifest modulepackage.Manifest, protoRoot string, prior eventCatalog) (eventCatalog, error) {
	catalog := eventCatalog{Schema: eventsCatalogSchema, Publishes: []eventCatalogPublish{}, Consumes: []eventCatalogConsume{}}
	namespaces := map[string]struct{}{}
	publishedTypes := map[string]struct{}{}
	priorByType := map[string]eventCatalogPublish{}
	for _, entry := range prior.Publishes {
		priorByType[entry.Type] = entry
	}

	type pendingConsume struct {
		contribution EventsContribution
		consume      ConsumedEvent
	}
	var consumes []pendingConsume

	for _, contribution := range contributions {
		if contribution.Schema != eventsContributionSchema {
			return eventCatalog{}, fmt.Errorf("events contribution schema must be %s", eventsContributionSchema)
		}
		if !logicalIDPattern.MatchString(contribution.Namespace) || isReserved(manifest.ReservedNamespaces, contribution.Namespace) {
			return eventCatalog{}, fmt.Errorf("events namespace %q is invalid or reserved", contribution.Namespace)
		}
		if _, duplicate := namespaces[contribution.Namespace]; duplicate {
			return eventCatalog{}, fmt.Errorf("events namespace %q is duplicated", contribution.Namespace)
		}
		namespaces[contribution.Namespace] = struct{}{}

		declaredQueues := map[string]struct{}{}
		for _, queue := range contribution.Queues {
			if !eventQueuePattern.MatchString(queue) {
				return eventCatalog{}, fmt.Errorf("events namespace %q declares invalid queue %q", contribution.Namespace, queue)
			}
			declaredQueues[queue] = struct{}{}
		}

		for _, published := range contribution.Publishes {
			if !eventTypePattern.MatchString(published.Type) {
				return eventCatalog{}, fmt.Errorf("event type %q is not a valid <namespace>.<aggregate>.<event> name", published.Type)
			}
			if !strings.HasPrefix(published.Type, contribution.Namespace+".") {
				return eventCatalog{}, fmt.Errorf("event type %q is outside namespace %q", published.Type, contribution.Namespace)
			}
			if _, exists := eventVisibilitySet[published.Visibility]; !exists {
				return eventCatalog{}, fmt.Errorf("event type %q has invalid visibility %q", published.Type, published.Visibility)
			}
			if _, duplicate := publishedTypes[published.Type]; duplicate {
				return eventCatalog{}, fmt.Errorf("event type %q is published by more than one namespace", published.Type)
			}
			fields, err := resolveProtoMessage(protoRoot, published.Schema)
			if err != nil {
				return eventCatalog{}, fmt.Errorf("event type %q schema: %w", published.Type, err)
			}
			major := majorFromSchema(published.Schema)
			if err := checkBreakingChange(published.Type, major, fields, priorByType); err != nil {
				return eventCatalog{}, err
			}
			publishedTypes[published.Type] = struct{}{}
			catalog.Publishes = append(catalog.Publishes, eventCatalogPublish{
				Type:       published.Type,
				Namespace:  contribution.Namespace,
				Schema:     published.Schema,
				Major:      major,
				Visibility: published.Visibility,
				Partition:  published.Partition,
				Retention:  published.Retention,
				Fields:     fields,
			})
		}

		for _, consume := range contribution.Consumes {
			if _, exists := eventDeliverySet[consume.Delivery]; !exists {
				return eventCatalog{}, fmt.Errorf("consumed event %q has invalid delivery %q", consume.Type, consume.Delivery)
			}
			if _, declared := declaredQueues[consume.Queue]; !declared {
				return eventCatalog{}, fmt.Errorf("consumed event %q names queue %q that namespace %q did not declare", consume.Type, consume.Queue, contribution.Namespace)
			}
			consumes = append(consumes, pendingConsume{contribution: contribution, consume: consume})
		}
	}

	for _, pending := range consumes {
		if _, registered := publishedTypes[pending.consume.Type]; !registered {
			return eventCatalog{}, fmt.Errorf("consumed event %q is not a registered published type", pending.consume.Type)
		}
		catalog.Consumes = append(catalog.Consumes, eventCatalogConsume{
			Type:       pending.consume.Type,
			Subscriber: pending.contribution.Namespace,
			Queue:      pending.consume.Queue,
			Delivery:   pending.consume.Delivery,
		})
	}

	sort.Slice(catalog.Publishes, func(i, j int) bool { return catalog.Publishes[i].Type < catalog.Publishes[j].Type })
	sort.Slice(catalog.Consumes, func(i, j int) bool {
		if catalog.Consumes[i].Type != catalog.Consumes[j].Type {
			return catalog.Consumes[i].Type < catalog.Consumes[j].Type
		}
		return catalog.Consumes[i].Subscriber < catalog.Consumes[j].Subscriber
	})
	return catalog, nil
}

// checkBreakingChange reuses the CONTRACT_VERSIONING.md rule that a field can
// never be removed or re-typed within one package major. A breaking change is
// only allowed behind a new major.
func checkBreakingChange(eventType string, major int, fields []eventField, prior map[string]eventCatalogPublish) error {
	previous, exists := prior[eventType]
	if !exists || previous.Major != major {
		return nil
	}
	current := map[int]eventField{}
	for _, field := range fields {
		current[field.Number] = field
	}
	for _, was := range previous.Fields {
		now, present := current[was.Number]
		if !present {
			return fmt.Errorf("event type %q removes field %q (%d) without a major bump", eventType, was.Name, was.Number)
		}
		if now.Type != was.Type {
			return fmt.Errorf("event type %q re-types field %d from %q to %q without a major bump", eventType, was.Number, was.Type, now.Type)
		}
	}
	return nil
}

func resolveProtoMessage(protoRoot, schema string) ([]eventField, error) {
	path, message, ok := strings.Cut(schema, "#")
	if !ok || path == "" || message == "" {
		return nil, fmt.Errorf("%q must be <path>#<Message>", schema)
	}
	if !safeRelativePath(path) || filepath.Ext(path) != ".proto" {
		return nil, fmt.Errorf("schema path %q is not a safe .proto reference", path)
	}
	file, err := os.Open(filepath.Join(protoRoot, filepath.FromSlash(path)))
	if err != nil {
		return nil, fmt.Errorf("open proto %q: %w", path, err)
	}
	defer file.Close()

	proto, err := protoparser.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("parse proto %q: %w", path, err)
	}
	for _, body := range proto.ProtoBody {
		if definition, ok := body.(*parser.Message); ok && definition.MessageName == message {
			return messageFields(definition)
		}
	}
	return nil, fmt.Errorf("message %q not found in %q", message, path)
}

func messageFields(message *parser.Message) ([]eventField, error) {
	var fields []eventField
	for _, body := range message.MessageBody {
		switch field := body.(type) {
		case *parser.Field:
			number, err := strconv.Atoi(field.FieldNumber)
			if err != nil {
				return nil, fmt.Errorf("field %q has non-numeric number %q", field.FieldName, field.FieldNumber)
			}
			fieldType := field.Type
			if field.IsRepeated {
				fieldType = "repeated " + fieldType
			}
			fields = append(fields, eventField{Name: field.FieldName, Number: number, Type: fieldType})
		case *parser.MapField:
			number, err := strconv.Atoi(field.FieldNumber)
			if err != nil {
				return nil, fmt.Errorf("field %q has non-numeric number %q", field.MapName, field.FieldNumber)
			}
			fields = append(fields, eventField{Name: field.MapName, Number: number, Type: fmt.Sprintf("map<%s, %s>", field.KeyType, field.Type)})
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Number < fields[j].Number })
	return fields, nil
}

func majorFromSchema(schema string) int {
	path, _, _ := strings.Cut(schema, "#")
	major := 1
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if match := versionDirPattern.FindStringSubmatch(segment); match != nil {
			if value, err := strconv.Atoi(match[1]); err == nil {
				major = value
			}
		}
	}
	return major
}

func readEventCatalog(outputRoot string) (eventCatalog, error) {
	body, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(EventCatalogOutput)))
	if os.IsNotExist(err) {
		return eventCatalog{}, nil
	}
	if err != nil {
		return eventCatalog{}, fmt.Errorf("read previous event catalog: %w", err)
	}
	var catalog eventCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return eventCatalog{}, fmt.Errorf("decode previous event catalog: %w", err)
	}
	return catalog, nil
}

func renderEventCatalog(catalog eventCatalog) (map[string][]byte, error) {
	catalogBody, err := marshalJSON(catalog)
	if err != nil {
		return nil, err
	}
	goBody, err := format.Source([]byte(renderEventCatalogGo(catalog)))
	if err != nil {
		return nil, fmt.Errorf("format Go event catalog: %w", err)
	}
	return map[string][]byte{
		EventCatalogOutput: catalogBody,
		EventGoOutput:      goBody,
	}, nil
}

func renderEventCatalogGo(catalog eventCatalog) string {
	var body strings.Builder
	body.WriteString("// Code generated by module-compose. DO NOT EDIT.\npackage eventcatalog\n\n")
	body.WriteString("type PublishedEvent struct {\n\tType string\n\tNamespace string\n\tSchema string\n\tMajor int\n\tVisibility string\n\tPartition string\n\tRetention string\n}\n\n")
	body.WriteString("type ConsumedEvent struct {\n\tType string\n\tSubscriber string\n\tQueue string\n\tDelivery string\n}\n\n")
	body.WriteString("var published = [...]PublishedEvent{\n")
	for _, entry := range catalog.Publishes {
		fmt.Fprintf(&body, "\t{Type: %q, Namespace: %q, Schema: %q, Major: %d, Visibility: %q, Partition: %q, Retention: %q},\n",
			entry.Type, entry.Namespace, entry.Schema, entry.Major, entry.Visibility, entry.Partition, entry.Retention)
	}
	body.WriteString("}\n\nvar consumed = [...]ConsumedEvent{\n")
	for _, entry := range catalog.Consumes {
		fmt.Fprintf(&body, "\t{Type: %q, Subscriber: %q, Queue: %q, Delivery: %q},\n",
			entry.Type, entry.Subscriber, entry.Queue, entry.Delivery)
	}
	body.WriteString("}\n\nfunc Published() []PublishedEvent {\n\treturn append([]PublishedEvent(nil), published[:]...)\n}\n\nfunc Consumed() []ConsumedEvent {\n\treturn append([]ConsumedEvent(nil), consumed[:]...)\n}\n")
	return body.String()
}
