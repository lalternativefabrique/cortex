package host

// conversationKey keeps each person's history of a context apart: a
// context id is the caller's to choose, and two people may pick the same.
func conversationKey(subject, contextID string) string {
	return subject + "\x00" + contextID
}
