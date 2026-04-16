package telegram

import (
	"encoding/json"
	"testing"
)

func TestMessageUnmarshalVoice(t *testing.T) {
	raw := `{
		"message_id": 1,
		"chat": {"id": 100, "type": "supergroup"},
		"date": 1700000000,
		"voice": {"file_id": "voice123", "duration": 5, "mime_type": "audio/ogg"}
	}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Voice == nil {
		t.Fatal("Voice should be non-nil")
	}
	if m.Voice.FileID != "voice123" {
		t.Errorf("Voice.FileID = %q, want %q", m.Voice.FileID, "voice123")
	}
	if m.Voice.Duration != 5 {
		t.Errorf("Voice.Duration = %d, want 5", m.Voice.Duration)
	}
}

func TestMessageUnmarshalAudio(t *testing.T) {
	raw := `{
		"message_id": 2,
		"chat": {"id": 100, "type": "supergroup"},
		"date": 1700000000,
		"audio": {"file_id": "audio456", "duration": 120, "file_name": "song.mp3", "mime_type": "audio/mpeg"}
	}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Audio == nil {
		t.Fatal("Audio should be non-nil")
	}
	if m.Audio.FileID != "audio456" {
		t.Errorf("Audio.FileID = %q, want %q", m.Audio.FileID, "audio456")
	}
	if m.Audio.FileName != "song.mp3" {
		t.Errorf("Audio.FileName = %q, want %q", m.Audio.FileName, "song.mp3")
	}
}

func TestMessageUnmarshalEditedMessage(t *testing.T) {
	raw := `{
		"update_id": 99,
		"edited_message": {
			"message_id": 3,
			"chat": {"id": 100, "type": "supergroup", "is_forum": true},
			"message_thread_id": 5,
			"date": 1700000000,
			"text": "corrected text"
		}
	}`
	var u Update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if u.EditedMessage == nil {
		t.Fatal("EditedMessage should be non-nil")
	}
	if u.EditedMessage.Text != "corrected text" {
		t.Errorf("text = %q, want %q", u.EditedMessage.Text, "corrected text")
	}
}

func TestUpdateUnmarshalPhotoLargest(t *testing.T) {
	raw := `{
		"message_id": 4,
		"chat": {"id": 100, "type": "supergroup"},
		"date": 1700000000,
		"photo": [
			{"file_id": "small", "width": 90, "height": 90},
			{"file_id": "medium", "width": 320, "height": 320},
			{"file_id": "large", "width": 800, "height": 800}
		]
	}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Photo) != 3 {
		t.Fatalf("expected 3 photos, got %d", len(m.Photo))
	}
	largest := m.Photo[len(m.Photo)-1]
	if largest.FileID != "large" {
		t.Errorf("largest photo = %q, want %q", largest.FileID, "large")
	}
}

func TestGetMeResponseParsing(t *testing.T) {
	raw := `{"ok":true,"result":{"id":123,"is_bot":true,"username":"testbot","first_name":"Test"}}`
	var resp apiResp[User]
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Result.Username != "testbot" {
		t.Errorf("unexpected: %+v", resp)
	}
	if resp.Result.ID != 123 {
		t.Errorf("ID = %d, want 123", resp.Result.ID)
	}
}

func TestSendMessageParamsSerialization(t *testing.T) {
	p := SendMessageParams{
		ChatID:          -1001234,
		MessageThreadID: 42,
		Text:            "hello",
		ReplyToMessageID: 10,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m["chat_id"].(float64) != -1001234 {
		t.Errorf("chat_id mismatch")
	}
	if m["message_thread_id"].(float64) != 42 {
		t.Errorf("message_thread_id mismatch")
	}
	if m["text"].(string) != "hello" {
		t.Errorf("text mismatch")
	}
}

func TestEditMessageTextParamsSerialization(t *testing.T) {
	p := EditMessageTextParams{
		ChatID:    -1001234,
		MessageID: 55,
		Text:      "updated",
	}
	data, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m["message_id"].(float64) != 55 {
		t.Errorf("message_id mismatch")
	}
}

func TestAPIRespError(t *testing.T) {
	raw := `{"ok":false,"description":"Bad Request: chat not found","error_code":400}`
	var resp apiResp[Message]
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
	if resp.ErrorCode != 400 {
		t.Errorf("error_code = %d, want 400", resp.ErrorCode)
	}
}
