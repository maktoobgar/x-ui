package web

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/pkg/translator"
	"x-ui/util/common"
	"x-ui/web/controller"
	"x-ui/web/job"
	"x-ui/web/network"
	"x-ui/web/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/language"
)

//go:embed assets/*
var assetsFS embed.FS

//go:embed html/*
var htmlFS embed.FS

var startTime = time.Now()

type wrapAssetsFS struct {
	embed.FS
}

func (f *wrapAssetsFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open("assets/" + name)
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFile{
		File: file,
	}, nil
}

type wrapAssetsFile struct {
	fs.File
}

func (f *wrapAssetsFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFileInfo{
		FileInfo: info,
	}, nil
}

type wrapAssetsFileInfo struct {
	fs.FileInfo
}

func (f *wrapAssetsFileInfo) ModTime() time.Time {
	return startTime
}

type Server struct {
	httpServer *http.Server
	listener   net.Listener

	index  *controller.IndexController
	server *controller.ServerController
	xui    *controller.XUIController

	xrayService    service.XrayService
	settingService service.SettingService
	inboundService service.InboundService

	cron *cron.Cron

	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Server) getHtmlFiles() ([]string, error) {
	files := make([]string, 0)
	dir, _ := os.Getwd()
	err := fs.WalkDir(os.DirFS(dir), "web/html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Server) getHtmlTemplate(funcMap template.FuncMap) (*template.Template, error) {
	t := template.New("").Funcs(funcMap)
	err := fs.WalkDir(htmlFS, "html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			newT, err := t.ParseFS(htmlFS, path+"/*.html")
			if err != nil {
				// ignore
				return nil
			}
			t = newT
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Server) initRouter() (*gin.Engine, error) {
	if config.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.DefaultWriter = io.Discard
		gin.DefaultErrorWriter = io.Discard
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.Default()

	secret, err := s.settingService.GetSecret()
	if err != nil {
		return nil, err
	}

	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	assetsBasePath := basePath + "assets/"

	store := cookie.NewStore(secret)
	engine.Use(sessions.Sessions("session", store))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", basePath)
	})
	engine.Use(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, assetsBasePath) {
			c.Header("Cache-Control", "max-age=31536000")
		}
	})
	err = s.initI18n(engine)
	if err != nil {
		return nil, err
	}

	if config.IsDebug() {
		// for develop
		files, err := s.getHtmlFiles()
		if err != nil {
			return nil, err
		}
		engine.LoadHTMLFiles(files...)
		engine.StaticFS(basePath+"assets", http.FS(os.DirFS("web/assets")))
	} else {
		// for prod
		t, err := s.getHtmlTemplate(engine.FuncMap)
		if err != nil {
			return nil, err
		}
		engine.SetHTMLTemplate(t)
		engine.StaticFS(basePath+"assets", http.FS(&wrapAssetsFS{FS: assetsFS}))
	}

	g := engine.Group(basePath)

	s.index = controller.NewIndexController(g)
	s.server = controller.NewServerController(g)
	s.xui = controller.NewXUIController(g)

	return engine, nil
}

// Sets translator in gin router
func (s *Server) initI18n(engine *gin.Engine) error {
	t, err := translator.New("translations", language.English, language.Persian)
	if err != nil {
		fmt.Println("here")
		return err
	}

	engine.Use(func(c *gin.Context) {
		lang := c.GetHeader("Accept-Language")
		c.Set("translator", t.GetTranslator(lang))
		c.Next()
	})

	return nil
}

// Starts xray and it's related cron scheduled tasks
func (s *Server) startTask() {
	err := s.xrayService.RestartXray(true)
	if err != nil {
		logger.Warning("start xray failed:", err)
	}
	// Check every 30 seconds if xray is running
	s.cron.AddJob("@every 30s", job.NewCheckXrayRunningJob())

	go func() {
		time.Sleep(time.Second * 5)
		// Traffic is counted every 10 seconds with
		// 5 seconds delay to give xray a time to start
		s.cron.AddJob("@every 10s", job.NewXrayTrafficJob())
	}()

	// Check for inbound traffic excess and expiration every 30 seconds
	s.cron.AddJob("@every 30s", job.NewCheckInboundJob())
	//? The traffic situation is prompted once a day, at 8:30 Shanghai time
	// isTgbotenabled, err := s.settingService.GetTgbotenabled()
	// if (err == nil) && (isTgbotenabled) {
	// 	runtime, err := s.settingService.GetTgbotRuntime()
	// 	if err != nil || runtime == "" {
	// 		logger.Errorf("Add NewStatsNotifyJob error[%s],Runtime[%s] invalid,wil run default", err, runtime)
	// 		runtime = "@daily"
	// 	}
	// 	logger.Infof("Tg notify enabled,run at %s", runtime)
	// 	_, err = s.cron.AddJob(runtime, job.NewStatsNotifyJob())
	// 	if err != nil {
	// 		logger.Warning("Add NewStatsNotifyJob error", err)
	// 		return
	// 	}
	// }
}

// Starts the x-ui dashboard server and xray service
func (s *Server) Start(port int) (err error) {
	// Close the server at the end if error happened
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()

	// Gets location like: Asia/Tehran
	loc, err := s.settingService.GetTimeLocation()
	if err != nil {
		return err
	}
	// Setup cron for scheduled jobs
	s.cron = cron.New(cron.WithLocation(loc), cron.WithSeconds())
	s.cron.Start()

	// Initialize routers
	engine, err := s.initRouter()
	if err != nil {
		return err
	}

	// Gets certifications if defined
	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return err
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return err
	}

	// Gets listening address if defined
	listen, err := s.settingService.GetListen()
	if err != nil {
		return err
	}

	// Gets listening port - default 54321
	if port == 0 {
		port, err = s.settingService.GetPort()
		if err != nil {
			return err
		}
	}

	// Defining listener with or without certificates
	listenAddr := net.JoinHostPort(listen, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			listener.Close()
			return err
		}
		c := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		listener = network.NewAutoHttpsListener(listener)
		listener = tls.NewListener(listener, c)
	}
	if certFile != "" || keyFile != "" {
		logger.Info("web server run https on", listener.Addr())
	} else {
		logger.Info("web server run http on", listener.Addr())
	}
	s.listener = listener

	// Starts xray and it's related cron scheduled tasks
	s.startTask()

	// Serve x-ui Dashboard
	s.httpServer = &http.Server{
		Handler: engine,
	}
	go func() {
		s.httpServer.Serve(listener)
	}()

	return nil
}

// Stops x-ui dashboard and xray service
func (s *Server) Stop() error {
	s.cancel()
	s.xrayService.StopXray()
	if s.cron != nil {
		s.cron.Stop()
	}
	var err1 error
	var err2 error
	if s.httpServer != nil {
		err1 = s.httpServer.Shutdown(s.ctx)
	}
	if s.listener != nil {
		err2 = s.listener.Close()
	}
	return common.Combine(err1, err2)
}

// Return context of the server
func (s *Server) GetCtx() context.Context {
	return s.ctx
}

// Return crontab of the server
func (s *Server) GetCron() *cron.Cron {
	return s.cron
}
