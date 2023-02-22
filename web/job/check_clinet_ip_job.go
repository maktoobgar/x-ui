package job

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	ss "strings"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/web/service"

	"gorm.io/gorm"
)

type CheckClientIpJob struct {
	xrayService    service.XrayService
	inboundService service.InboundService
	penalty        int
}

var job *CheckClientIpJob

func NewCheckClientIpJob(penalty int) *CheckClientIpJob {
	job = new(CheckClientIpJob)
	job.penalty = penalty * 2
	return job
}

func (j *CheckClientIpJob) Run() {
	logger.Debug("Check Client IP Job...")
	emails := activateInboundsAfterPenalty(j.penalty)
	processLogFile(emails)
}

func processLogFile(emails map[string]bool) {
	accessLogPath := GetAccessLogPath()
	if accessLogPath == "" {
		logger.Warning("xray log not init in config.json")
		return
	}

	data, err := os.ReadFile(accessLogPath)
	InboundClientIps := make(map[string][]string)
	checkError(err)

	// clean log
	if err := os.Truncate(GetAccessLogPath(), 0); err != nil {
		checkError(err)
	}

	lines := ss.Split(string(data), "\n")
	for _, line := range lines {
		ipRegx, _ := regexp.Compile(`[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`)
		emailRegx, _ := regexp.Compile(`email:.+`)

		matchesIp := ipRegx.FindString(line)
		if len(matchesIp) > 0 {
			ip := string(matchesIp)
			if ip == "127.0.0.1" || ip == "1.1.1.1" {
				continue
			}

			matchesEmail := emailRegx.FindString(line)
			if matchesEmail == "" {
				continue
			}
			matchesEmail = ss.TrimSpace(ss.Split(matchesEmail, "email: ")[1])
			if _, ok := emails[matchesEmail]; !ok {
				if InboundClientIps[matchesEmail] != nil {
					if contains(InboundClientIps[matchesEmail], ip) {
						continue
					}
					InboundClientIps[matchesEmail] = append(InboundClientIps[matchesEmail], ip)
				} else {
					InboundClientIps[matchesEmail] = append(InboundClientIps[matchesEmail], ip)
				}
			}
		}
	}
	err = ClearInboudClientIps()
	if err != nil {
		return
	}

	var inboundsClientIps []*model.InboundClientIps
	for clientEmail, ips := range InboundClientIps {
		inboundClientIps := GetInboundClientIps(clientEmail, ips)
		if inboundClientIps != nil {
			inboundsClientIps = append(inboundsClientIps, inboundClientIps)
		}
	}

	err = AddInboundsClientIps(inboundsClientIps)
	checkError(err)
}

type getEmail struct {
	Email string `json:"email"`
}

type justToGetEmail struct {
	Clients []getEmail `json:"clients"`
}

// Returns emails of inactive accounts
func activateInboundsAfterPenalty(penalty int) map[string]bool {
	inbounds := GetInactivePenaltyInbounds()
	activated := map[int]bool{}
	for i := 0; i < len(inbounds); i++ {
		element := inbounds[i]
		if element.Penalty < penalty {
			updateInboudPenaltyBy1(element.Id, element.Penalty)
		} else {
			activated[i] = true
			activateInboundAfterFullPenalty(element.Id)
		}
	}

	output := map[string]bool{}
	for i := 0; i < len(inbounds); i++ {
		if ok := activated[i]; !ok {
			toGetEmail := &justToGetEmail{}
			json.Unmarshal([]byte(inbounds[i].Settings), toGetEmail)
			if len(toGetEmail.Clients) != 0 {
				email := toGetEmail.Clients[0].Email
				output[email] = true
			} else {
				continue
			}
		}
	}

	return output
}

func updateInboudPenaltyBy1(id int, currentPenalty int) {
	db := database.GetDB()
	err := db.Model(model.Inbound{}).
		Where("id = ? and enable = ?", id, false).
		Update("penalty", currentPenalty+1).Error
	if err != nil {
		logger.Error("couldn't update inactive penalty inbound count by 1: ", err)
	}
}

func activateInboundAfterFullPenalty(id int) {
	db := database.GetDB()
	var inbound *model.Inbound
	err := db.Model(model.Inbound{}).
		Where("id = ? and enable = ?", id, false).Find(&inbound).Error
	if err != nil {
		logger.Error("couldn't find inbound with id: ", id)
		return
	} else {
		job.xrayService.SetToNeedRestart()
	}

	inbound.Enable = true
	inbound.Penalty = -1
	db.Save(&inbound)

	logger.Warning("enable inbound after finished penalty with id: ", id)
}

func GetAccessLogPath() string {
	config, err := os.ReadFile("bin/config.json")
	checkError(err)

	jsonConfig := map[string]interface{}{}
	err = json.Unmarshal([]byte(config), &jsonConfig)
	checkError(err)
	if jsonConfig["log"] != nil {
		jsonLog := jsonConfig["log"].(map[string]interface{})
		if jsonLog["access"] != nil {

			accessLogPath := jsonLog["access"].(string)

			return accessLogPath
		}
	}
	return ""

}

func checkError(e error) {
	if e != nil {
		logger.Warning("client ip job err:", e)
	}
}
func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

func ClearInboudClientIps() error {
	db := database.GetDB()
	err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.InboundClientIps{}).Error
	checkError(err)
	return err
}

func GetInboundClientIps(clientEmail string, ips []string) *model.InboundClientIps {
	jsonIps, err := json.Marshal(ips)
	if err != nil {
		return nil
	}

	inboundClientIps := &model.InboundClientIps{}
	inboundClientIps.ClientEmail = clientEmail
	inboundClientIps.Ips = string(jsonIps)

	inbound, err := GetInboundByEmail(clientEmail)
	if err != nil {
		return nil
	}
	limitIpRegx, _ := regexp.Compile(`"limitIp": .+`)
	limitIpMactch := limitIpRegx.FindString(inbound.Settings)
	limitIpMactchs := ss.Split(limitIpMactch, `"limitIp": `)
	if len(limitIpMactchs) > 1 {
		limitIpMactch = limitIpMactchs[1]
	} else {
		limitIpMactch = "0"
	}
	limitIp, err := strconv.Atoi(limitIpMactch)
	if err != nil {
		return nil
	}
	if limitIp < len(ips) && limitIp != 0 && inbound.Enable {
		DisableInbound(inbound.Id)
	}

	return inboundClientIps
}

func AddInboundsClientIps(inboundsClientIps []*model.InboundClientIps) error {
	if len(inboundsClientIps) == 0 {
		return nil
	}
	db := database.GetDB()
	tx := db.Begin()

	err := tx.Save(inboundsClientIps).Error
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func GetInboundByEmail(clientEmail string) (*model.Inbound, error) {
	db := database.GetDB()
	var inbounds *model.Inbound
	err := db.Model(model.Inbound{}).Where("settings LIKE ?", "%"+clientEmail+"%").Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func DisableInbound(id int) error {
	db := database.GetDB()
	var inbound *model.Inbound
	err := db.Model(model.Inbound{}).
		Where("id = ? and enable = ?", id, true).Find(&inbound).Error
	if err != nil {
		logger.Error("couldn't find inbound with id: ", id)
	}

	inbound.Enable = false
	inbound.Penalty = 0
	db.Save(&inbound)
	logger.Warning("disable inbound with id:", id)

	if err == nil {
		job.xrayService.SetToNeedRestart()
	}

	return err
}

func GetInactivePenaltyInbounds() []*model.Inbound {
	db := database.GetDB()
	inbounds := []*model.Inbound{}
	err := db.Model(model.Inbound{}).
		Where("enable = ? and penalty > ?", false, -1).
		Find(&inbounds).Error
	if err != nil {
		logger.Error("couldn't find inactive penalty inbounds: ", err)
	}

	return inbounds
}
