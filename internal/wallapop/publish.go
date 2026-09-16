// Item publishing and editing. Shapes recorded live 2026-09-15 from the web
// upload flow against the owner's test account (see docs/cli-spec.md §item):
// a client UUID identifies the upload; pictures go to
// POST /api/v3/upload/{uuid}/pictures as multipart image parts (204); the
// listing itself is POST /api/v3/items as multipart (image files plus an
// `item` JSON part) with Accept application/vnd.upload-v2+json, answering
// 200 {"id"}. Edits are PUT /api/v3/items/{hash} in the same multipart shape
// (or plain JSON when no image changes).
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
	"net/url"
	"strconv"
	"strings"
)

// quoteEscaper matches what mime/multipart does to its own filenames: a
// basename holding a quote or a backslash would otherwise close the header
// early and leave the image part unreadable.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, `\"`)

// UploadAccept is the media type the item write endpoints route on. Without
// it POST /api/v3/items answers 405.
const UploadAccept = "application/vnd.upload-v2+json"

// UploadImage is one picture attached to a create or edit. Data is the raw
// file bytes; ContentType should be image/jpeg, image/png or image/webp.
type UploadImage struct {
	Name        string
	Data        []byte
	ContentType string
}

// CreateInput is everything `item create` sends. Price is euros;
// CategoryLeaf must be a leaf category id; Attrs holds category-specific
// attributes (cars: brand, model, year) already validated against the
// category's attribute list.
type CreateInput struct {
	Title        string
	Description  string
	Price        float64
	CategoryLeaf int
	CategoryRoot int
	Condition    string
	Lat, Lng     float64
	Attrs        map[string]string
	Images       []UploadImage
}

// EditInput overlays onto the item's current state: nil fields stay as they
// were. Attrs merge over the current attributes. Images, when non-empty,
// replace the listing's pictures.
type EditInput struct {
	Title       *string
	Description *string
	Price       *float64
	Condition   *string
	Attrs       map[string]string
	Images      []UploadImage
	// CategoryLeaf is the listing's leaf category when the caller already
	// resolved one (the --attr path does). Zero means "work it out".
	CategoryLeaf int
}

// itemPayload is the `item` JSON part. CategoryLeafID is a string on the
// wire (recorded: "24221").
type itemPayload struct {
	Attributes     map[string]any `json:"attributes"`
	CategoryLeafID string         `json:"category_leaf_id"`
	ApplyDiscount  bool           `json:"apply_discount"`
	Location       map[string]any `json:"location"`
	UploadID       string         `json:"upload_id,omitempty"`
}

func (in CreateInput) payload(uploadID string) itemPayload {
	attrs := map[string]any{
		"title":        in.Title,
		"description":  in.Description,
		"condition":    in.Condition,
		"price_amount": in.Price,
	}
	for k, v := range in.Attrs {
		attrs[k] = v
	}
	return itemPayload{
		Attributes:     attrs,
		CategoryLeafID: strconv.Itoa(in.CategoryLeaf),
		Location: map[string]any{
			"latitude": in.Lat, "longitude": in.Lng, "approximated": false,
		},
		UploadID: uploadID,
	}
}

// CreateItem publishes a listing and returns it as the API reports it. At
// least one image is required: imageless creates are rejected upstream.
func (c *Client) CreateItem(ctx context.Context, in CreateInput) (Item, error) {
	if len(in.Images) == 0 {
		return Item{}, &Error{Kind: KindUsage, Endpoint: "POST /api/v3/items", Msg: "at least one --image is required to publish a listing"}
	}
	uploadID := newUUID()
	if err := c.uploadComponents(ctx, in, uploadID); err != nil {
		return Item{}, err
	}
	for _, img := range in.Images {
		if err := c.uploadPicture(ctx, uploadID, img); err != nil {
			return Item{}, err
		}
	}
	payload := in.payload(uploadID)
	var out struct {
		ID string `json:"id"`
	}
	if err := c.writeItem(ctx, http.MethodPost, "/api/v3/items", UploadAccept, payload, in.Images, &out); err != nil {
		return Item{}, err
	}
	if out.ID == "" {
		return Item{}, &Error{Kind: KindAPIChanged, Endpoint: "POST /api/v3/items", Msg: "wallapop returned no item id"}
	}
	// The listing exists from here on. A fresh hash can take a moment to be
	// readable, and failing the command over that would invite a retry that
	// publishes the listing twice, so fall back to what was just sent.
	it, err := c.Item(ctx, out.ID)
	if err != nil {
		if c.Notice != nil {
			c.Notice("published " + out.ID + ", but reading it back failed: " + err.Error())
		}
		return in.published(out.ID), nil
	}
	// A page 404 makes Client.Item infer "expired", which is right for a
	// listing Wallapop has hidden and wrong for one published seconds ago:
	// there the page simply has not propagated yet.
	it.Expired = false
	return it, nil
}

// published describes a listing straight from what was sent, for the window
// where the API has the id but cannot serve the item yet.
func (in CreateInput) published(id string) Item {
	return Item{
		Hash:        id,
		Title:       in.Title,
		Description: in.Description,
		Price:       in.Price,
		Currency:    "EUR",
		CategoryID:  in.CategoryLeaf,
		Condition:   in.Condition,
		Location:    Location{Lat: in.Lat, Lng: in.Lng},
	}
}

// uploadComponents opens the web's upload session for this listing. The
// response carries the upload form schema, which the CLI does not need.
func (c *Client) uploadComponents(ctx context.Context, in CreateInput, uploadID string) error {
	body := map[string]any{
		"fields": map[string]any{
			"summary": in.Title, "category_leaf_id": strconv.Itoa(in.CategoryLeaf),
			"root_category_id": strconv.Itoa(in.CategoryRoot),
		},
		"mode": map[string]any{"action": "upload", "id": uploadID},
	}
	_, err := c.do(ctx, request{method: http.MethodPost, base: c.APIBase, path: "/api/v3/items/upload/components", body: body, auth: true}, nil)
	return err
}

// uploadPicture attaches one image to the upload session.
func (c *Client) uploadPicture(ctx context.Context, uploadID string, img UploadImage) error {
	var out any
	return c.writeItem(ctx, http.MethodPost, "/api/v3/upload/"+uploadID+"/pictures", "application/json", nil, []UploadImage{img}, &out)
}

// EditItem overlays the input onto the item's current state and writes it
// back. ownerHash is the active profile's user hash; items listed by another
// account are refused before writing, like the other seller actions.
func (c *Client) EditItem(ctx context.Context, hash, ownerHash string, in EditInput) (Item, error) {
	current, err := c.Item(ctx, hash)
	if err != nil {
		return Item{}, err
	}
	if ownerHash != "" && current.SellerHash != "" && current.SellerHash != ownerHash {
		return Item{}, &Error{Kind: KindUsage, Endpoint: "PUT /api/v3/items/" + current.Hash, Msg: "this listing belongs to another account; edit is refused"}
	}
	// The write replaces the whole attribute set, so the listing's existing
	// category attributes (a car's brand/model/year) go back untouched or a
	// title-only edit would wipe them.
	attrs := map[string]any{}
	for k, v := range current.Attributes {
		attrs[k] = v
	}
	payload := map[string]any{"attributes": attrs}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	title, description, condition := current.Title, current.Description, current.Condition
	price := current.Price
	set(&title, in.Title)
	set(&description, in.Description)
	set(&condition, in.Condition)
	if in.Price != nil {
		price = *in.Price
	}
	attrs["title"], attrs["description"], attrs["condition"], attrs["price_amount"] = title, description, condition, price
	for k, v := range in.Attrs {
		attrs[k] = v
	}
	leaf, err := c.editLeafID(ctx, in, current)
	if err != nil {
		return Item{}, err
	}
	payload["category_leaf_id"] = leaf
	// `item edit` exposes no location flag, so the listing's own coordinates
	// go back unchanged. When the item detail carries none, the field is left
	// out rather than resubmitted as 0,0, which would move the listing into
	// the Atlantic.
	if current.Location.Lat != 0 || current.Location.Lng != 0 {
		payload["location"] = map[string]any{"latitude": current.Location.Lat, "longitude": current.Location.Lng, "approximated": false}
	}
	var out any
	if err := c.writeItem(ctx, http.MethodPut, "/api/v3/items/"+current.Hash, UploadAccept, payload, in.Images, &out); err != nil {
		return Item{}, ownWriteError(err)
	}
	// The edit has landed. Like a create, failing the command because the
	// read-back raced the API would invite a pointless retry, so fall back to
	// the pre-edit listing with the overlay applied.
	it, err := c.Item(ctx, current.Hash)
	if err != nil {
		if c.Notice != nil {
			c.Notice("edited " + current.Hash + ", but reading it back failed: " + err.Error())
		}
		current.Title, current.Description, current.Condition = title, description, condition
		current.Price = price
		current.Attributes = attrs
		if len(in.Images) > 0 {
			// The replacement landed, so the old URLs are gone. Reporting
			// them would be worse than reporting none.
			current.Images = nil
		}
		return current, nil
	}
	return it, nil
}

// editLeafID recovers the listing's leaf category for the write payload.
// Item detail exposes the taxonomy path, not a leaf id, so the path is
// matched back against the create tree, which CreateCategories names with the
// same separator. A single-level taxonomy needs no lookup: its one id is the
// leaf. A nested path that will not resolve is an error rather than a guess,
// because sending a root id as the leaf recategorises the listing.
func (c *Client) editLeafID(ctx context.Context, in EditInput, current Item) (string, error) {
	if in.CategoryLeaf > 0 {
		return strconv.Itoa(in.CategoryLeaf), nil
	}
	if !strings.Contains(current.Category, ">") && current.CategoryID > 0 {
		return strconv.Itoa(current.CategoryID), nil
	}
	cats, err := c.CreateCategories(ctx)
	if err != nil {
		return "", err
	}
	cat, err := ResolveCreateCategory(cats, current.Category)
	if err != nil {
		return "", &Error{
			Kind:     KindAPIChanged,
			Endpoint: "PUT /api/v3/items/" + current.Hash,
			Msg:      "cannot tell which leaf category " + current.Hash + " sits in (" + current.Category + "), and editing it would have to resend one. Report this listing",
		}
	}
	return strconv.Itoa(cat.LeafID), nil
}

// writeItem sends a multipart item write (image files plus an `item` JSON
// part, or JSON alone when there are no files) with the upload accept the
// endpoints route on, and decodes a JSON response into out (may be nil).
func (c *Client) writeItem(ctx context.Context, method, path, accept string, payload any, images []UploadImage, out any) error {
	endpoint := method + " " + path
	var bodyBytes []byte
	contentType := ""
	if len(images) > 0 {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, img := range images {
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
		}
		if payload != nil {
			raw, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			if err := w.WriteField("item", string(raw)); err != nil {
				return err
			}
		}
		if err := w.Close(); err != nil {
			return err
		}
		bodyBytes = buf.Bytes()
		contentType = w.FormDataContentType()
	} else if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyBytes = raw
		contentType = "application/json"
	}
	if c.Tokens == nil {
		return &Error{Kind: KindAuth, Endpoint: endpoint, Msg: "this command needs a logged-in profile. Run `wallapop auth login`"}
	}
	tok, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	c.Redact(tok)
	// The body is buffered, so the request can be rebuilt for the CloudFront
	// user-agent retry below.
	build := func() (*http.Request, error) {
		var rd io.Reader
		if bodyBytes != nil {
			rd = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.APIBase+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.currentUA())
		req.Header.Set("Accept", accept)
		req.Header.Set("X-DeviceOS", "0")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	}
	req, err := build()
	if err != nil {
		return err
	}
	c.debugf("> %s %s (%d bytes)", method, c.redact(c.APIBase+path), len(bodyBytes))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.netError(endpoint, err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if err != nil {
		return c.netError(endpoint, err)
	}
	c.debugf("< %d %s (%d bytes)", resp.StatusCode, endpoint, len(raw))

	// Same one-shot CloudFront fallback Client.do performs: writes go through
	// this path instead, so without it an upload or publish dies on a 403 the
	// rest of the client recovers from.
	if resp.StatusCode == http.StatusForbidden && isCloudFrontBlock(raw) && !c.browserUAActive() {
		c.setBrowserUA()
		if c.Notice != nil {
			c.Notice("wallapop rejected the wallapop-cli user agent; retrying with a browser user agent for this run")
		}
		req, err = build()
		if err != nil {
			return err
		}
		resp, err = c.HTTP.Do(req)
		if err != nil {
			return c.netError(endpoint, err)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return c.netError(endpoint, err)
		}
		c.debugf("< %d %s (%d bytes, browser UA)", resp.StatusCode, endpoint, len(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(endpoint, resp.StatusCode, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &Error{Kind: KindAPIChanged, Status: resp.StatusCode, Endpoint: endpoint, Body: "decode: " + err.Error(), Msg: "wallapop returned a response the cli does not understand"}
		}
	}
	return nil
}

// CreateCategory is a leaf category the upload flow accepts, with the
// attribute keys the web validates for it (cars: brand, model, year).
type CreateCategory struct {
	LeafID int
	RootID int
	Name   string
	Attrs  []string
}

type rawCreateCategory struct {
	ID            int                 `json:"id"`
	Name          string              `json:"name"`
	Attributes    attrMap             `json:"attributes"`
	Subcategories []rawCreateCategory `json:"subcategories"`
}

// attrMap decodes the attribute table, skipping metadata entries whose
// values are not objects (recorded live: "excluded": [] on fashion leaves).
type attrMap map[string]attrDef

func (m *attrMap) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := attrMap{}
	for k, v := range raw {
		var d attrDef
		if err := json.Unmarshal(v, &d); err == nil {
			out[k] = d
		}
	}
	*m = out
	return nil
}

type attrDef struct {
	Title string `json:"title"`
}

// CreateCategories lists the leaf categories the upload flow accepts with
// their validated attribute keys. Attributes inherit down the tree: a leaf
// merges its ancestors' lists, since verticals carry shared keys.
func (c *Client) CreateCategories(ctx context.Context) ([]CreateCategory, error) {
	var raw struct {
		Categories []rawCreateCategory `json:"categories"`
	}
	q := url.Values{"context": {"create"}}
	if err := c.getJSON(ctx, "/api/v3/categories", q, false, &raw); err != nil {
		return nil, err
	}
	var out []CreateCategory
	var walk func(node rawCreateCategory, root int, path string, inherited map[string]bool)
	walk = func(node rawCreateCategory, root int, path string, inherited map[string]bool) {
		if root == 0 {
			root = node.ID
		}
		merged := map[string]bool{}
		for k := range inherited {
			merged[k] = true
		}
		for k := range node.Attributes {
			merged[k] = true
		}
		name := node.Name
		if path != "" {
			// Same separator as taxonomyPath so item edit can resolve
			// the listing's category name back to a leaf.
			name = path + " > " + name
		}
		if len(node.Subcategories) == 0 {
			attrs := make([]string, 0, len(merged))
			for k := range merged {
				attrs = append(attrs, k)
			}
			out = append(out, CreateCategory{LeafID: node.ID, RootID: root, Name: name, Attrs: attrs})
			return
		}
		for _, sub := range node.Subcategories {
			walk(sub, root, name, merged)
		}
	}
	for _, top := range raw.Categories {
		walk(top, 0, "", nil)
	}
	if len(out) == 0 {
		return nil, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/categories", Msg: "wallapop returned no leaf categories"}
	}
	return out, nil
}

// ResolveCreateCategory matches ref against leaf ids and (case-insensitive)
// full or leaf names. Ambiguous or unknown refs are usage errors.
func ResolveCreateCategory(cats []CreateCategory, ref string) (CreateCategory, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		for _, cat := range cats {
			if cat.LeafID == id {
				return cat, nil
			}
		}
		return CreateCategory{}, &Error{Kind: KindUsage, Endpoint: "item create", Msg: "unknown category id " + ref}
	}
	lower := ref
	var hits []CreateCategory
	for _, cat := range cats {
		if strings.EqualFold(cat.Name, lower) || strings.EqualFold(leafName(cat.Name), lower) {
			hits = append(hits, cat)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return CreateCategory{}, &Error{Kind: KindUsage, Endpoint: "item create", Msg: "category " + ref + " is ambiguous; use the leaf id"}
	}
	return CreateCategory{}, &Error{Kind: KindUsage, Endpoint: "item create", Msg: "unknown category " + ref}
}

func leafName(path string) string {
	if i := strings.LastIndex(path, ">"); i >= 0 {
		return strings.TrimSpace(path[i+1:])
	}
	return strings.TrimSpace(path)
}
