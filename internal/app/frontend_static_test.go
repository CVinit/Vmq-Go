package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var htmlAssetTagPattern = regexp.MustCompile(`(?is)<(script|link|img)\b[^>]*(?:src|href)\s*=\s*"([^"]*)"[^>]*>`)
var scriptBlockPattern = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)

func TestLayuiJQueryModuleLoadsAfterLayuiRuntime(t *testing.T) {
	webRoot := filepath.Join("..", "..", "src", "main", "webapp")
	err := filepath.WalkDir(webRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		page := string(content)
		moduleIndex := strings.Index(page, "layui/lay/modules/jquery.js")
		if moduleIndex == -1 {
			return nil
		}

		beforeModule := page[:moduleIndex]
		if !strings.Contains(beforeModule, "layui/layui.js") && !strings.Contains(beforeModule, "layui/layui.all.js") {
			t.Errorf("%s loads Layui jquery module before the Layui runtime", path)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk web root: %v", err)
	}
}

func TestHTMLLocalStaticAssetsExistWithExactCase(t *testing.T) {
	webRoot := filepath.Join("..", "..", "src", "main", "webapp")
	err := filepath.WalkDir(webRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		page := stripInlineScripts(stripHTMLComments(string(content)))
		for _, match := range htmlAssetTagPattern.FindAllStringSubmatch(page, -1) {
			tag := strings.ToLower(match[1])
			attrValue := strings.TrimSpace(match[2])
			if attrValue == "" || isExternalOrDynamicAsset(attrValue) {
				continue
			}
			if tag == "link" && !strings.Contains(strings.ToLower(match[0]), "stylesheet") && !looksLikeStaticIcon(attrValue) {
				continue
			}

			assetPath := resolveHTMLAssetPath(webRoot, filepath.Dir(path), attrValue)
			if _, statErr := os.Stat(assetPath); statErr != nil {
				t.Errorf("%s references missing static asset %q resolved as %s", path, attrValue, assetPath)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk web root: %v", err)
	}
}

func TestAdminShellPublishesGlobalJQueryBeforeLoadingFragments(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "aaa.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin shell page: %v", err)
	}
	page := string(content)
	loadIndex := strings.Index(page, `$("#LAY_preview").load("main.html");`)
	if loadIndex == -1 {
		t.Fatal("expected admin shell to load main.html into LAY_preview")
	}

	beforeLoad := page[:loadIndex]
	if !strings.Contains(beforeLoad, "window.jQuery = window.jQuery || $;") ||
		!strings.Contains(beforeLoad, "window.$ = window.$ || $;") {
		t.Fatal("expected admin shell to expose Layui jquery globally before loading HTML fragments")
	}
}

func TestAlipayGuideOnlyLaunchesWalletWithTargetURL(t *testing.T) {
	path := filepath.Join("..", "..", "src", "main", "webapp", "payPage", "go_alipay.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read alipay guide page: %v", err)
	}
	page := string(content)
	if !strings.Contains(page, "if (url) {") || !strings.Contains(page, "AlipayWallet.open({") {
		t.Fatal("expected alipay guide page to launch the wallet only when a target URL is present")
	}
}

func stripHTMLComments(content string) string {
	for {
		start := strings.Index(content, "<!--")
		if start == -1 {
			return content
		}
		end := strings.Index(content[start+4:], "-->")
		if end == -1 {
			return content[:start]
		}
		content = content[:start] + content[start+4+end+3:]
	}
}

func stripInlineScripts(content string) string {
	return scriptBlockPattern.ReplaceAllStringFunc(content, func(block string) string {
		openingTagEnd := strings.Index(block, ">")
		if openingTagEnd == -1 {
			return ""
		}
		openingTag := strings.ToLower(block[:openingTagEnd])
		if strings.Contains(openingTag, " src=") || strings.Contains(openingTag, "\tsrc=") {
			return block[:openingTagEnd+1] + "</script>"
		}
		return ""
	})
}

func isExternalOrDynamicAsset(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "//") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "javascript:") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "tel:") ||
		strings.HasPrefix(lower, "#")
}

func looksLikeStaticIcon(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasSuffix(lower, ".ico") ||
		strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".svg")
}

func resolveHTMLAssetPath(webRoot, pageDir, value string) string {
	cleanValue := strings.Split(strings.Split(value, "?")[0], "#")[0]
	if strings.HasPrefix(cleanValue, "/") {
		return filepath.Join(webRoot, strings.TrimPrefix(cleanValue, "/"))
	}
	return filepath.Join(pageDir, cleanValue)
}
