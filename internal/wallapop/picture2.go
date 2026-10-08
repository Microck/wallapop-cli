package wallapop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
)

// uploadItemPicture attaches one picture to an already-created item, the way
// the web's upload queue does: POST /api/v3/items/{id}/picture2, multipart
// with a plain `order` field (1-based position in the gallery, the create
// call having consumed position 0) plus the image under the `image` field.
// Recorded from the Angular upload app (chunk-2K77EQS6.js, processQueue).
func (c *Client) uploadItemPicture(ctx context.Context, itemID string, order int, img UploadImage) error {
	endpoint := "POST /api/v3/items/" + itemID + "/picture2"
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("order", strconv.Itoa(order)); err != nil {
		return err
	}
	ct := img.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	fw, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="image"; filename="%s"`, quoteEscaper.Replace(img.Name))},
		"Content-Type":        {ct},
	})
	if err != nil {
		return err
	}
	if _, err := fw.Write(img.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	if c.Tokens == nil {
		return &Error{Kind: KindAuth, Endpoint: endpoint, Msg: "this command needs a logged-in profile. Run `wallapop auth login`"}
	}
	tok, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	c.Redact(tok)

	build := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/api/v3/items/"+itemID+"/picture2", bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.currentUA())
		req.Header.Set("Accept", UploadAccept)
		req.Header.Set("X-DeviceOS", "0")
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	}
	req, err := build()
	if err != nil {
		return err
	}
	// The endpoint answers 2xx with a body the client does not need (the web
	// ignores it too), so no JSON decoding.
	_, err = c.doRequest(ctx, req, build, endpoint, nil)
	return err
}

// doRequest mirrors Client.do's transport handling for pre-built requests.
// Minimal shim: send, retry once with the browser UA on CloudFront blocks,
// decode JSON into out and classify failures.
func (c *Client) doRequest(ctx context.Context, req *http.Request, rebuild func() (*http.Request, error), endpoint string, out any) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.netError(endpoint, err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if err != nil {
		return nil, c.netError(endpoint, err)
	}
	c.debugf("< %d %s (%d bytes)", resp.StatusCode, endpoint, len(raw))
	if resp.StatusCode == http.StatusForbidden && isCloudFrontBlock(raw) && !c.browserUAActive() {
		c.setBrowserUA()
		if c.Notice != nil {
			c.Notice("wallapop rejected the wallapop-cli user agent; retrying with a browser user agent for this run")
		}
		req, err = rebuild()
		if err != nil {
			return nil, err
		}
		resp, err = c.HTTP.Do(req)
		if err != nil {
			return nil, c.netError(endpoint, err)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.netError(endpoint, err)
		}
		c.debugf("< %d %s (%d bytes, browser UA)", resp.StatusCode, endpoint, len(raw))
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok {
		return resp, c.statusError(endpoint, resp.StatusCode, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp, &Error{Kind: KindAPIChanged, Status: resp.StatusCode, Endpoint: endpoint, Body: "decode: " + err.Error() + "; body: " + c.snippet(raw), Msg: "wallapop returned a response the cli does not understand"}
		}
	}
	return resp, nil
}
