package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
)

// API evidence is fully materialized. Physical records inherit equal parent
// references and derive only IDs whose exact equality was checked at write.
// Explicit overrides (including empty values) preserve unknown/foreign facts.
type storedEvent struct {
	snapshot.TrajectoryEvent
	ID        *string            `json:"id,omitempty"`
	SessionID *string            `json:"session_id,omitempty"`
	Entities  []storedOccurrence `json:"entities,omitempty"`
}

type storedOccurrence struct {
	ID          *string                       `json:"id,omitempty"`
	EntityID    *string                       `json:"entity_id,omitempty"`
	EventID     *string                       `json:"event_id,omitempty"`
	SessionID   *string                       `json:"session_id,omitempty"`
	Kind        string                        `json:"kind"`
	Literal     string                        `json:"literal"`
	Label       *string                       `json:"label,omitempty"`
	Scope       *string                       `json:"scope,omitempty"`
	Predicate   string                        `json:"predicate"`
	NativeField string                        `json:"native_field"`
	Source      *snapshot.TrajectorySourceRef `json:"source,omitempty"`
}

func storedOverride(value, inherited string) *string {
	if value == inherited {
		return nil
	}
	return &value
}
func storedString(value *string, inherited string) string {
	if value == nil {
		return inherited
	}
	return *value
}
func sourceEventID(source snapshot.TrajectorySourceRef) string {
	return fmt.Sprintf("e.%s.%s.%s.%d.%s", source.ID, source.Generation, strconv.FormatInt(source.Offset, 36), source.Block, source.Digest)
}
func occurrenceEntityID(kind, scope, literal string) string {
	return "ent." + digest([]byte(kind+"\x00"+scope+"\x00"+literal))
}
func occurrenceID(eventID, entityID, predicate, nativeField string) string {
	return "occ." + digest([]byte(eventID+"\x00"+entityID+"\x00"+predicate+"\x00"+nativeField))
}

func encodeEventStored(e snapshot.TrajectoryEvent) ([]byte, error) {
	stored := storedEvent{TrajectoryEvent: e, ID: storedOverride(e.ID, sourceEventID(e.Source)), SessionID: storedOverride(e.SessionID, "s."+e.Source.ID+"."+e.Source.Generation)}
	for _, o := range e.Entities {
		item := storedOccurrence{ID: storedOverride(o.ID, occurrenceID(e.ID, o.EntityID, o.Predicate, o.NativeField)), EntityID: storedOverride(o.EntityID, occurrenceEntityID(o.Kind, o.Scope, o.Literal)), EventID: storedOverride(o.EventID, e.ID), SessionID: storedOverride(o.SessionID, e.SessionID), Kind: o.Kind, Literal: o.Literal, Label: storedOverride(o.Label, o.Literal), Scope: storedOverride(o.Scope, "session:"+e.SessionID), Predicate: o.Predicate, NativeField: o.NativeField}
		if o.Source != e.Source {
			copy := o.Source
			item.Source = &copy
		}
		stored.Entities = append(stored.Entities, item)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeStored(raw)
	if err != nil {
		return nil, err
	}
	logical, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(logical) > maxStoredValue {
		return nil, fmt.Errorf("trajectory event exceeds storage bound")
	}
	// Retain the expanded byte count so query and analysis budgets remain
	// independent of physical reference factoring and compression.
	compressed := len(encoded) < len(raw)
	body := raw
	if compressed {
		body = encoded[8:]
	}
	framed := make([]byte, 12+len(body))
	copy(framed, valueMagic)
	framed[3] = 3
	if compressed {
		framed[3] = 5
	}
	binary.BigEndian.PutUint32(framed[4:8], uint32(len(raw)))
	binary.BigEndian.PutUint32(framed[8:12], uint32(len(logical)))
	copy(framed[12:], body)
	return framed, nil
}

func decodeEventStored(value []byte) (snapshot.TrajectoryEvent, error) {
	var stored storedEvent
	raw, err := decodeStored(value)
	if err != nil {
		return stored.TrajectoryEvent, err
	}
	if err = json.Unmarshal(raw, &stored); err != nil {
		return stored.TrajectoryEvent, err
	}
	e := stored.TrajectoryEvent
	e.ID = storedString(stored.ID, sourceEventID(e.Source))
	e.SessionID = storedString(stored.SessionID, "s."+e.Source.ID+"."+e.Source.Generation)
	e.Entities = nil
	for _, item := range stored.Entities {
		scope := storedString(item.Scope, "session:"+e.SessionID)
		entityID := storedString(item.EntityID, occurrenceEntityID(item.Kind, scope, item.Literal))
		o := snapshot.TrajectoryEntityOccurrence{ID: storedString(item.ID, occurrenceID(e.ID, entityID, item.Predicate, item.NativeField)), EntityID: entityID, EventID: storedString(item.EventID, e.ID), SessionID: storedString(item.SessionID, e.SessionID), Kind: item.Kind, Literal: item.Literal, Label: storedString(item.Label, item.Literal), Scope: scope, Predicate: item.Predicate, NativeField: item.NativeField, Source: e.Source}
		if item.Source != nil {
			o.Source = *item.Source
		}
		e.Entities = append(e.Entities, o)
	}
	return e, nil
}

func currentEventEncoding(value []byte) bool {
	return len(value) >= 12 && value[0] == 0 && value[1] == 'A' && value[2] == 'L' && (value[3] == 5 || value[3] == 3)
}

// Old compact values are accepted only by the bounded migration. This also
// classifies event headers before validating their complete frame length.
func compactEventEncoding(value []byte) bool {
	return len(value) >= 4 && value[0] == 0 && value[1] == 'A' && value[2] == 'L' && (value[3] == 2 || value[3] == 3 || value[3] == 5)
}
