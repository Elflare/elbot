package qqofficial

import "encoding/json"

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11

	intentGroupAndC2C = 1 << 25

	eventReady            = "READY"
	eventResumed          = "RESUMED"
	eventC2CMessageCreate = "C2C_MESSAGE_CREATE"
)

type payload struct {
	ID   string          `json:"id,omitempty"`
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d,omitempty"`
	Seq  *int64          `json:"s,omitempty"`
	Type string          `json:"t,omitempty"`
}

type helloData struct {
	HeartbeatInterval int64 `json:"heartbeat_interval"`
}

type identifyData struct {
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	Shard      [2]int            `json:"shard"`
	Properties map[string]string `json:"properties,omitempty"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
}

type readyData struct {
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	User      readyUser `json:"user"`
	Shard     [2]int    `json:"shard"`
}

type readyUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

type gatewayResponse struct {
	URL string `json:"url"`
}

type accessTokenRequest struct {
	AppID        string `json:"appId"`
	ClientSecret string `json:"clientSecret"`
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   any    `json:"expires_in"`
	Code        int    `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

type apiErrorResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type c2cMessage struct {
	ID               string              `json:"id"`
	Author           c2cAuthor           `json:"author"`
	Content          string              `json:"content"`
	Timestamp        string              `json:"timestamp"`
	Attachments      []messageAttachment `json:"attachments"`
	MessageReference *messageReference   `json:"message_reference,omitempty"`
}

type c2cAuthor struct {
	UserOpenID string `json:"user_openid"`
}

type messageAttachment struct {
	URL string `json:"url"`
}

type messageReference struct {
	MessageID string `json:"message_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type messageToCreate struct {
	Content  string           `json:"content,omitempty"`
	MsgType  int              `json:"msg_type"`
	Markdown *messageMarkdown `json:"markdown,omitempty"`
	Keyboard *messageKeyboard `json:"keyboard,omitempty"`
	Ark      *messageArk      `json:"ark,omitempty"`
	Media    *messageMedia    `json:"media,omitempty"`
	MsgID    string           `json:"msg_id,omitempty"`
	EventID  string           `json:"event_id,omitempty"`
	MsgSeq   int              `json:"msg_seq,omitempty"`
	IsWakeup bool             `json:"is_wakeup,omitempty"`
}

type messageMarkdown struct {
	Content    string                 `json:"content,omitempty"`
	TemplateID int                    `json:"template_id,omitempty"`
	Params     []messageMarkdownParam `json:"params,omitempty"`
}

type messageMarkdownParam struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

type messageMedia struct {
	FileInfo string `json:"file_info"`
}

type messageKeyboard struct {
	ID      string                 `json:"id,omitempty"`
	Content *inlineKeyboardContent `json:"content,omitempty"`
}

type inlineKeyboardContent struct {
	Rows     []keyboardRow `json:"rows"`
	BotAppID string        `json:"bot_appid,omitempty"`
}

type keyboardRow struct {
	Buttons []keyboardButton `json:"buttons"`
}

type keyboardButton struct {
	ID         string               `json:"id,omitempty"`
	RenderData keyboardRenderData   `json:"render_data"`
	Action     keyboardButtonAction `json:"action"`
}

type keyboardRenderData struct {
	Label        string `json:"label"`
	VisitedLabel string `json:"visited_label"`
	Style        int    `json:"style,omitempty"`
}

type keyboardButtonAction struct {
	Type          int                `json:"type"`
	Permission    keyboardPermission `json:"permission"`
	ClickLimit    int                `json:"click_limit,omitempty"`
	UnsupportTips string             `json:"unsupport_tips,omitempty"`
	Data          string             `json:"data"`
	Enter         bool               `json:"enter,omitempty"`
	Reply         bool               `json:"reply,omitempty"`
}

type keyboardPermission struct {
	Type int `json:"type"`
}

type messageArk struct {
	TemplateID int            `json:"template_id"`
	KV         []messageArkKV `json:"kv"`
}

type messageArkKV struct {
	Key   string          `json:"key"`
	Value string          `json:"value,omitempty"`
	Obj   []messageArkObj `json:"obj,omitempty"`
}

type messageArkObj struct {
	ObjKV []messageArkObjKV `json:"obj_kv"`
}

type messageArkObjKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type messageResponse struct {
	ID        string `json:"id"`
	Timestamp any    `json:"timestamp,omitempty"`
}

type uploadFileRequest struct {
	FileType   int    `json:"file_type"`
	URL        string `json:"url,omitempty"`
	SrvSendMsg bool   `json:"srv_send_msg"`
	FileData   string `json:"file_data,omitempty"`
}

type uploadFileResponse struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
	ID       string `json:"id,omitempty"`
}
