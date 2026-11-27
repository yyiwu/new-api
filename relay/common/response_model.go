package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RestoreResponseModel changes only protocol model fields at the client boundary.
// Keep the upstream body intact for usage accounting and model diagnostics.
func RestoreResponseModel(data []byte, requested string) ([]byte, error) {
	if requested == "" || !gjson.ValidBytes(data) {
		return data, nil
	}
	paths := []string{"model", "modelVersion"}
	switch event := gjson.GetBytes(data, "type").String(); {
	case strings.HasPrefix(event, "response."):
		paths = append(paths, "response.model")
	case event == "message_start":
		paths = append(paths, "message.model")
	case event == "session.created" || event == "session.updated":
		paths = append(paths, "session.model")
	}
	for _, path := range paths {
		value := gjson.GetBytes(data, path)
		if value.Type != gjson.String || value.String() == requested {
			continue
		}
		var err error
		data, err = sjson.SetBytes(data, path, requested)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// ResponseModel records upstream declarations before response conversion. It is
// diagnostic only: it must never change routing, pricing, or downstream output.
// Only the three names are stored; whether they disagree is computed on demand
// so every consumer applies the current comparison rule to old rows as well.
type ResponseModel struct {
	RequestedModel string `json:"requested_model"`
	UpstreamModel  string `json:"upstream_model"`
	ReturnedModel  string `json:"returned_model"`
}

// matches reports whether an upstream declaration is compatible with the
// requested or upstream model: equal ignoring case, a dated or variant name
// that extends it, or the same name behind a provider path such as
// "deepseek/deepseek-v4.1-flash".
func (r *ResponseModel) matches(model string) bool {
	returned := strings.ToLower(model)
	for _, expected := range []string{r.RequestedModel, r.UpstreamModel} {
		expected = strings.ToLower(expected)
		if expected != "" && (strings.HasPrefix(returned, expected) || strings.HasSuffix(returned, expected)) {
			return true
		}
	}
	return false
}

// Mismatch reports whether the retained upstream declaration disagrees with
// both the requested and upstream models.
func (r *ResponseModel) Mismatch() bool {
	return r != nil && r.ReturnedModel != "" && !r.matches(r.ReturnedModel)
}

// ObserveResponseModel retains the first differing model for inspection, with
// mismatches taking priority over provider-path, prefix, or case-only
// differences. A later matching or empty event cannot erase it. Only observe
// upstream declarations, never models synthesized by a response converter.
func (info *RelayInfo) ObserveResponseModel(model string) {
	if info == nil || strings.TrimSpace(model) == "" {
		return
	}
	if info.ResponseModel == nil {
		info.ResponseModel = &ResponseModel{
			RequestedModel: info.OriginModelName,
			UpstreamModel:  info.GetUpstreamModelName(),
		}
	}
	observation := info.ResponseModel
	if observation.Mismatch() {
		return
	}
	if observation.matches(model) && observation.ReturnedModel != "" &&
		observation.ReturnedModel != observation.RequestedModel && observation.ReturnedModel != observation.UpstreamModel {
		return
	}
	observation.ReturnedModel = model
}
