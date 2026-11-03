package store

import (
	"encoding/json"
	"time"
)

// UpdateConversationMetadata merges execution preferences without replacing other metadata.
func (s *SQLite) UpdateConversationMetadata(id string, updates map[string]string) error {
	conversation, err := s.GetConversation(id)
	if err != nil {
		return err
	}
	if conversation == nil {
		return nil
	}
	if conversation.Metadata == nil {
		conversation.Metadata = map[string]string{}
	}
	for key, value := range updates {
		conversation.Metadata[key] = value
	}
	encoded, _ := json.Marshal(conversation.Metadata)
	_, err = s.db.Exec(`UPDATE conversations SET metadata = ?, updated_at = ? WHERE id = ?`, string(encoded), time.Now(), id)
	return err
}
