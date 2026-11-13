package management

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigV8ContextKey marks operations registered by the v8 API that save configuration.
const ConfigV8ContextKey = "management.config-v8"

// ConfigV8 serves the persisted configuration using v8 names. GET is a view;
// only a successful configuration mutation migrates the stored document.
//
// Reads return an ETag holding the SHA-256 of the on-disk config bytes.
// Writes honour an optional If-Match against that token, so a client can
// detect that another writer saved after its read (412) instead of silently
// overwriting the change. Without If-Match, writes stay unconditional.
func (h *Handler) ConfigV8(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "config file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	diskHash := config.ComputeSourceHash(raw)
	if c.Request.Method != http.MethodGet && !ifMatchSatisfied(c.Request.Header.Values("If-Match"), diskHash) {
		c.Header("ETag", configV8ETag(diskHash))
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "precondition_failed"})
		return
	}
	data, _, err := config.NormalizeConfigLayout(raw, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config"})
		return
	}
	root := doc.Content[0]
	path := strings.Trim(c.Param("path"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	yamlRequest := strings.HasSuffix(c.FullPath(), "/config.yaml")
	if c.Request.Method == http.MethodGet {
		config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
		value := configV8Node(root, parts)
		if value == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("ETag", configV8ETag(diskHash))
		if yamlRequest {
			c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
			return
		}
		var result any
		if err = value.Decode(&result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode_failed"})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	before := cloneConfigV8Node(root)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	if c.Request.Method == http.MethodDelete {
		if len(parts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_config"})
			return
		}
		if !deleteConfigV8Path(root, parts) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	} else {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		if !yamlRequest && !json.Valid(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		var update yaml.Node
		if err = yaml.Unmarshal(body, &update); err != nil || len(update.Content) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		stripAPIKeysAuthIndexesFromUpdate(parts, &update)
		if len(parts) == 0 && update.Content[0].Kind != yaml.MappingNode {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config_must_be_object"})
			return
		}
		if len(parts) == 0 {
			if err = config.NormalizeV8ConfigAliases(update.Content[0]); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
				return
			}
		}
		dst := root
		for _, part := range parts {
			if part == "" || dst.Kind != yaml.MappingNode {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
				return
			}
			next := configV8Node(dst, []string{part})
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
			}
			dst = next
		}
		if c.Request.Method == http.MethodPatch {
			mergeConfigV8Patch(dst, update.Content[0])
		} else {
			if dst.Kind == yaml.ScalarNode && update.Content[0].Kind == yaml.ScalarNode {
				update.Content[0].HeadComment = dst.HeadComment
				update.Content[0].LineComment = dst.LineComment
				update.Content[0].FootComment = dst.FootComment
			}
			*dst = *update.Content[0]
		}
	}
	if err = config.NormalizeV8ConfigAliases(root); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if !yamlRequest && c.Request.Method != http.MethodDelete {
		preserveV8TURNSecrets(root, before)
	}
	for _, field := range []string{"credentials/concurrency/lifecycle-config-revision", "credentials/concurrency/observation-barrier-revision", "plugins/auth-revision"} {
		old := configV8Node(before, strings.Split(field, "/"))
		next := configV8Node(root, strings.Split(field, "/"))
		var a, b any
		if old != nil {
			_ = old.Decode(&a)
		}
		if next != nil {
			_ = next.Decode(&b)
		}
		if !reflect.DeepEqual(a, b) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_only_field", "field": field})
			return
		}
	}
	stripAPIKeysAuthIndexesFromRoot(root)
	config.NormalizeCollectionNodeStyles(root)
	data, err = yaml.Marshal(&doc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if _, err = config.ParseConfigBytes(data); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = config.ValidateV8Config(data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	data, _, err = config.NormalizeConfigLayout(data, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = WriteConfig(h.configFilePath, data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	newCfg, errReload := config.LoadConfig(h.configFilePath)
	if errReload != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload_failed", "message": errReload.Error()})
		return
	}
	if saved, errSaved := os.ReadFile(h.configFilePath); errSaved == nil {
		c.Header("ETag", configV8ETag(config.ComputeSourceHash(saved)))
	}
	h.cfg = newCfg
	c.JSON(http.StatusOK, gin.H{"status": "ok", "config-version": 8})
}

func configV8ETag(hash string) string {
	return `"` + hash + `"`
}

func ifMatchSatisfied(values []string, currentHash string) bool {
	if len(values) == 0 {
		return true
	}
	field := strings.TrimSpace(strings.Join(values, ","))
	if field == "*" {
		return true
	}
	for _, tag := range splitEntityTagList(field) {
		tag = strings.TrimSpace(tag)
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && !strings.Contains(tag[1:len(tag)-1], `"`) && tag[1:len(tag)-1] == currentHash {
			return true
		}
	}
	return false
}

func splitEntityTagList(field string) []string {
	parts := []string{}
	start := 0
	quoted := false
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, field[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, field[start:])
}

var v8ICEServersPath = []string{"oauth", "providers", "codex", "live-media-relay", "ice-servers"}

func preserveV8TURNSecrets(root, before *yaml.Node) {
	next := configV8Node(root, v8ICEServersPath)
	previous := configV8Node(before, v8ICEServersPath)
	if next == nil || previous == nil || next.Kind != yaml.SequenceNode || previous.Kind != yaml.SequenceNode {
		return
	}
	matched := make([]bool, len(previous.Content))
	for _, server := range next.Content {
		urls := configV8Node(server, []string{"urls"})
		if urls == nil {
			continue
		}
		var newURLs []string
		if urls.Decode(&newURLs) != nil {
			continue
		}
		for i, old := range previous.Content {
			if matched[i] {
				continue
			}
			oldURLs := configV8Node(old, []string{"urls"})
			var previousURLs []string
			if oldURLs == nil || oldURLs.Decode(&previousURLs) != nil || !reflect.DeepEqual(newURLs, previousURLs) {
				continue
			}
			matched[i] = true
			for _, name := range []string{"username", "credential"} {
				if configV8Node(server, []string{name}) == nil {
					if secret := configV8Node(old, []string{name}); secret != nil {
						server.Content = append(server.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, cloneConfigV8Node(secret))
					}
				}
			}
			break
		}
	}
}

func configV8Node(root *yaml.Node, parts []string) *yaml.Node {
	for _, part := range parts {
		if root == nil || root.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == part {
				next = root.Content[i+1]
				break
			}
		}
		root = next
	}
	return root
}

func deleteConfigV8Path(root *yaml.Node, parts []string) bool {
	if root == nil || root.Kind != yaml.MappingNode || len(parts) == 0 {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) > 1 {
			child := root.Content[i+1]
			if !deleteConfigV8Path(child, parts[1:]) {
				return false
			}
			if len(child.Content) > 0 {
				return true
			}
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		return true
	}
	return false
}

func cloneConfigV8Node(node *yaml.Node) *yaml.Node {
	copied := *node
	copied.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copied.Content[i] = cloneConfigV8Node(child)
	}
	return &copied
}

func mergeConfigV8Patch(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		if dst.Kind == yaml.ScalarNode && src.Kind == yaml.ScalarNode {
			copied := *src
			copied.HeadComment, copied.LineComment, copied.FootComment = dst.HeadComment, dst.LineComment, dst.FootComment
			*dst = copied
			return
		}
		*dst = *src
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		if old := configV8Node(dst, []string{key.Value}); old != nil {
			mergeConfigV8Patch(old, value)
		} else {
			dst.Content = append(dst.Content, key, value)
		}
	}
}

func deleteYAMLMapKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func stripAPIKeysAuthIndexesFromGroup(group *yaml.Node) {
	if group == nil || group.Kind != yaml.MappingNode {
		return
	}
	deleteYAMLMapKey(group, "auth_index")
	deleteYAMLMapKey(group, "auth-index")
	keysNode := configV8Node(group, []string{"keys"})
	if keysNode != nil && keysNode.Kind == yaml.SequenceNode {
		for _, keyNode := range keysNode.Content {
			if keyNode != nil && keyNode.Kind == yaml.MappingNode {
				deleteYAMLMapKey(keyNode, "auth_index")
				deleteYAMLMapKey(keyNode, "auth-index")
			}
		}
	}
}

func stripAPIKeysAuthIndexesFromGroups(groupsNode *yaml.Node) {
	if groupsNode == nil {
		return
	}
	if groupsNode.Kind == yaml.SequenceNode {
		for _, group := range groupsNode.Content {
			stripAPIKeysAuthIndexesFromGroup(group)
		}
	} else if groupsNode.Kind == yaml.MappingNode {
		stripAPIKeysAuthIndexesFromGroup(groupsNode)
	}
}

func stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode *yaml.Node) {
	if apiKeysNode == nil || apiKeysNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(apiKeysNode.Content); i += 2 {
		stripAPIKeysAuthIndexesFromGroups(apiKeysNode.Content[i+1])
	}
}

func stripAPIKeysAuthIndexesFromRoot(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	apiKeysNode := configV8Node(root, []string{"api-keys"})
	if apiKeysNode != nil {
		stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode)
	}
}

func stripAPIKeysAuthIndexesFromUpdate(parts []string, update *yaml.Node) {
	if update == nil || len(update.Content) == 0 {
		return
	}
	node := update.Content[0]
	if len(parts) == 0 {
		stripAPIKeysAuthIndexesFromRoot(node)
		return
	}
	if parts[0] != "api-keys" {
		return
	}
	if len(parts) == 1 {
		stripAPIKeysAuthIndexesFromProvidersMap(node)
		return
	}
	stripAPIKeysAuthIndexesFromGroups(node)
}
