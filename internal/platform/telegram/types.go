package telegram

import "encoding/json"

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	EditedMessage *message       `json:"edited_message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type chatMember struct {
	Status string `json:"status"`
}

type getChatMemberRequest struct {
	ChatID int64 `json:"chat_id"`
	UserID int64 `json:"user_id"`
}

type message struct {
	MessageID      int64       `json:"message_id"`
	Date           int64       `json:"date"`
	From           *user       `json:"from"`
	Chat           chat        `json:"chat"`
	Text           string      `json:"text"`
	Caption        string      `json:"caption"`
	Photo          []photoSize `json:"photo"`
	Document       *document   `json:"document"`
	ReplyToMessage *message    `json:"reply_to_message"`
}

type photoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size"`
}

type document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	MIMEType     string `json:"mime_type"`
	FileSize     int64  `json:"file_size"`
}

type fileInfo struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	FilePath     string `json:"file_path"`
}

type sentMessage struct {
	MessageID int64 `json:"message_id"`
}

type sendMessageRequest struct {
	ChatID            int64        `json:"chat_id"`
	Text              string       `json:"text"`
	ParseMode         string       `json:"parse_mode,omitempty"`
	ReplyToMessageID  int64        `json:"reply_to_message_id,omitempty"`
	ReplyMarkup       *replyMarkup `json:"reply_markup,omitempty"`
	DisableWebPreview bool         `json:"disable_web_page_preview,omitempty"`
}

type editMessageTextRequest struct {
	ChatID      int64        `json:"chat_id"`
	MessageID   int64        `json:"message_id"`
	Text        string       `json:"text"`
	ParseMode   string       `json:"parse_mode,omitempty"`
	ReplyMarkup *replyMarkup `json:"reply_markup,omitempty"`
}

type answerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

type inputRichMessage struct {
	HTML                string `json:"html,omitempty"`
	Markdown            string `json:"markdown,omitempty"`
	IsRTL               bool   `json:"is_rtl,omitempty"`
	SkipEntityDetection bool   `json:"skip_entity_detection,omitempty"`
}

type replyParameters struct {
	MessageID int64 `json:"message_id"`
}

type sendRichMessageRequest struct {
	ChatID          int64            `json:"chat_id"`
	RichMessage     inputRichMessage `json:"rich_message"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup     *replyMarkup     `json:"reply_markup,omitempty"`
}

type sendRichMessageDraftRequest struct {
	ChatID      int64            `json:"chat_id"`
	DraftID     int64            `json:"draft_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

type getFileRequest struct {
	FileID string `json:"file_id"`
}

type setMyCommandsRequest struct {
	Commands []botCommand `json:"commands"`
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type replyMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func mustRawJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
