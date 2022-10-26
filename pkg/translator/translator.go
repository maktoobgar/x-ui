package translator

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

const (
	// Format of how file names are specified in translation
	// folder storage. it's like: message.en.json or message.fa.toml
	fileFormat string = "translation.%v.%v"
)

type (
	Translator interface {
		GetTranslator(language string) func(string) string
	}
)

type TranslatorPack struct {
	bundle         *i18n.Bundle
	addedLanguages []string
	localizers     map[string]*i18n.Localizer
}

var (
	filesFS   embed.FS
	filesRoot string
	// The main translator object that will be used in the whole services
	translator *TranslatorPack = &TranslatorPack{
		addedLanguages: []string{},
		localizers:     map[string]*i18n.Localizer{},
	}
	// If you wanna add another format to support in your translation files,
	// you have to add your new format's extention to supportedFormats by hand
	// and give unmarshal of that format to `bundle` variable. like what I did
	// in second line of Setup function for toml format.
	supportedFormats     = []string{"json", "toml"}
	errLocalizerNotFound = fmt.Errorf("localizer not found")
)

// Initialization of the translation.
//
// You can get your language Tag with using "golang.org/x/text/language"
// library like: language.English
func New(translations embed.FS, rootAddress string, defaultLanguage language.Tag, languages ...language.Tag) (Translator, error) {
	filesFS = translations
	filesRoot = rootAddress
	translator.bundle = i18n.NewBundle(defaultLanguage)
	translator.bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	languages = append(languages, defaultLanguage)

	err := loadLanguages(languages...)
	if err != nil {
		return nil, err
	}

	return translator, nil
}

// Translates to requested language and if only the language
// is not added before with `loadLanguages` or `Setup` functions,
// `localizer not found` error returns.
//
// You can get your language string code with using "golang.org/x/text/language"
// library like: language.English.String()
func (translator *TranslatorPack) GetTranslator(language string) func(string) string {
	localizer, err := returnLocalizer(language)
	if err != nil {
		return func(messageID string) string { return messageID }
	}

	return func(messageID string) string {
		return translateLocal(localizer, &i18n.LocalizeConfig{
			MessageID: messageID,
			DefaultMessage: &i18n.Message{
				ID: messageID,
			},
		})
	}
}

// Loads embed translations contents into translator
func loadFS(root string) error {
	err := fs.WalkDir(filesFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			content, err := filesFS.ReadFile(filepath.Join(filesRoot, d.Name()))
			if err != nil {
				return fmt.Errorf("couldn't read %s file, file format should be like: %s", filepath.Join(root, d.Name()), fmt.Sprintf(fileFormat, translator.addedLanguages, supportedFormats))
			}
			_, err = translator.bundle.ParseMessageFileBytes(content, d.Name())
			if err != nil {
				return fmt.Errorf("couldn't parse content of %s file, file format should be like: %s", filepath.Join(root, d.Name()), fmt.Sprintf(fileFormat, translator.addedLanguages, supportedFormats))
			}
		} else {
			if path != root {
				return loadFS(path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return err
}

// Loads translation files and if no file for language determined
// in specified address in `address` variable, return error
//
// # Also appends new languages to addedLanguages variable
//
// After that it calls loadLocalizers to create localizers for translation
// to different languages
func loadLanguages(languages ...language.Tag) error {
	if translator.bundle == nil {
		return fmt.Errorf("please call Setup function first")
	}

	for _, lang := range languages {
		translator.addedLanguages = append(translator.addedLanguages, lang.String())
	}

	err := loadFS(filesRoot)
	if err != nil {
		return err
	}

	loadLocalizers()

	return nil
}

// Translates to preferred localizer.
//
// No error will be returned and if no translation been found,
// same `MessageID` in `config` variable returns.
//
// You can get your desired `localizer` from `returnLocalizer` function.
func translateLocal(localizer *i18n.Localizer, config *i18n.LocalizeConfig) string {
	config.DefaultMessage = &i18n.Message{
		ID:    config.MessageID,
		One:   config.MessageID,
		Other: config.MessageID,
	}

	msg, _ := localizer.Localize(config)
	return msg
}

// Returns preferred localizer based on language code you passed.
//
// If there is no localizer with that language, `localizer not found`
// error returns.
//
// You can get your language string code with using "golang.org/x/text/language"
// library like: language.English.String()
func returnLocalizer(language string) (*i18n.Localizer, error) {
	localizer, ok := translator.localizers[language]
	if ok {
		return localizer, nil
	}

	return nil, errLocalizerNotFound
}

// Creates localizers for translation to different languages.
func loadLocalizers() {
	for _, lang := range translator.addedLanguages {
		_, ok := translator.localizers[lang]
		if ok {
			continue
		}
		translator.localizers[lang] = i18n.NewLocalizer(translator.bundle, lang)
	}
}
