package gui

import (
	"os"
	"path/filepath"
)

// VerifyResult is returned to JS by verifyIntegrity.
type VerifyResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// verifyIntegrity checks that key installed files exist and are non-empty.
// It uses the install dir determined by the config passed to SetVerifyConfig.
func (g *GUI) verifyIntegrity() map[string]interface{} {
	g.mu.Lock()
	cfg := g.verifyCfg
	g.mu.Unlock()

	if cfg == nil {
		return map[string]interface{}{"ok": false, "message": "Конфигурация не загружена"}
	}

	type check struct {
		name string
		path string
	}
	checks := []check{
		{"Клиент (Syntee Helper.exe)", cfg.ClientExePath()},
		{"Обновщик (updater.exe)", filepath.Join(cfg.CurrentDir(), "updater.exe")},
		{"Конфигурация (launcher.json)", filepath.Join(cfg.Dir(), "launcher.json")},
	}

	var missing []string
	for _, c := range checks {
		info, err := os.Stat(c.path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			missing = append(missing, c.name)
		}
	}

	// Count files in current/ dir
	entries, _ := os.ReadDir(cfg.CurrentDir())
	if len(entries) < 5 {
		missing = append(missing, "Файлы клиента (меньше 5 файлов в current/)")
	}

	if len(missing) > 0 {
		msg := "Проблемы найдены:\n"
		for _, m := range missing {
			msg += "  • " + m + "\n"
		}
		msg += "\nРекомендуется переустановить приложение."
		return map[string]interface{}{"ok": false, "message": msg}
	}

	return map[string]interface{}{
		"ok":      true,
		"message": "Все файлы на месте. Целостность подтверждена.\nФайлов в current/: " + intToStr(len(entries)),
	}
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
