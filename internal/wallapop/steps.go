package wallapop

import (
	"context"
	"encoding/json"
	"net/http"
)

// StepsRaw posts an arbitrary payload to the steps wizard endpoint and
// returns the raw JSON response. Debug/experimental.
func (c *Client) StepsRaw(ctx context.Context, payload any) (json.RawMessage, error) {
	var out json.RawMessage
	_, err := c.do(ctx, request{
		method: http.MethodPost,
		base:   c.APIBase,
		path:   "/api/v3/steps",
		body:   payload,
		auth:   true,
	}, &out)
	return out, err
}

// StepsGetRaw GETs an arbitrary API path and returns the raw JSON.
func (c *Client) StepsGetRaw(ctx context.Context, path string) (json.RawMessage, error) {
	var out json.RawMessage
	_, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: path, auth: true}, &out)
	return out, err
}

// StepsUploadRaw posts image parts (multipart, field "image") to an
// arbitrary path, e.g. the photo step's picture_upload.path.
func (c *Client) StepsUploadRaw(ctx context.Context, path string, images []UploadImage) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.writeItem(ctx, http.MethodPost, path, "application/json", nil, images, &out)
	return out, err
}
