package spoofer

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"github.com/wtg/shuttletracker"
	"github.com/wtg/shuttletracker/log"
)

// Spoofer creates "fake" (spoofed) updates at regular intervals from data in the spoof_data folder
type Spoofer struct {
	cfg           Config
	spoofInterval time.Duration
	SpoofUpdates  bool
	spoofIndexes  map[int64]int
	updates       map[int64][]shuttletracker.Location
	updateID      int64
	ms            shuttletracker.ModelService
	mutex         *sync.Mutex
	sm            *sync.Mutex
	subscribers   []func(*shuttletracker.Location)
}

// Configuration for Spoofer; determines whether or not spoofed updates will be created and the interval
// at which it will occur
type Config struct {
	SpoofUpdates  bool
	SpoofInterval string
}

// Creates a new Spoofer
func New(cfg Config, ms shuttletracker.ModelService) (*Spoofer, error) {
	spoofer := &Spoofer{
		cfg:         cfg,
		ms:          ms,
		mutex:       &sync.Mutex{},
		sm:          &sync.Mutex{},
		subscribers: []func(*shuttletracker.Location){},
	}

	interval, err := time.ParseDuration(cfg.SpoofInterval)
	if err != nil {
		return nil, err
	}
	spoofer.spoofInterval = interval
	spoofer.SpoofUpdates = cfg.SpoofUpdates
	spoofer.spoofIndexes = make(map[int64]int)
	spoofer.updates = make(map[int64][]shuttletracker.Location)
	spoofer.updateID = 1

	return spoofer, nil
}

// Creates a new config object from the values in the config file.
func NewConfig(v *viper.Viper) *Config {
	cfg := &Config{
		SpoofUpdates:  false,
		SpoofInterval: "10s",
	}
	v.SetDefault("spoof.spoofUpdates", cfg.SpoofUpdates)
	v.SetDefault("spoof.spoofInterval", cfg.SpoofInterval)
	return cfg
}

// Don't know why, but this needs to be called after NewConfig is called in order to
// actually read the values from the config file. Otherwise, they fallback to the
// default values.
func BackupConfig(v *viper.Viper) *Config {
	cfg := &Config{
		SpoofUpdates:  false,
		SpoofInterval: "10s",
	}
	if v.IsSet("spoof.spoofupdates") {
		cfg.SpoofUpdates = v.GetBool("spoof.spoofupdates")
	}
	if v.IsSet("spoof.spoofinterval") {
		cfg.SpoofInterval = v.GetString("spoof.spoofinterval")
	}
	return cfg
}

// Run spoofer forever.
func (s *Spoofer) Run() {
	if s.SpoofUpdates {
		log.Debug("Spoofer started.")
		ticker := time.Tick(s.spoofInterval)

		// Parse all update data
		s.parseUpdates()

		// Do one initial spoof
		s.spoof()

		// Spoof updates for each vehicle every spoofInterval
		for range ticker {
			s.spoof()
		}
	}
}

// Sequentially reads and caches all JSON data to create updates from
func (s *Spoofer) parseUpdates() {
	wd, err := os.Getwd()
	if err != nil {
		log.WithError(err).Errorf("Error getting working directory")
		return
	}
	files, err := ioutil.ReadDir(wd + "/spoof_data/")
	if err != nil {
		log.WithError(err).Errorf("Error finding spoof files")
		return
	}
	for _, f := range files {
		// Parse this file only if it is a JSON file
		extensionIndex := strings.LastIndex(f.Name(), ".")
		if !f.IsDir() && extensionIndex > -1 && f.Name()[extensionIndex+1:] == "json" {
			vehiclefile, err := os.Open(wd + "/spoof_data/" + f.Name())
			if err != nil {
				log.WithError(err).Errorf("Error opening spoof data file %s", f.Name())
				return
			}
			bytes, err := ioutil.ReadAll(vehiclefile)
			if err != nil {
				log.WithError(err).Errorf("Error reading spoof file %s", f.Name())
			}
			var updates []shuttletracker.Location
			json.Unmarshal(bytes, &updates)

			// Only cache data for this vehicle if it has updates
			if len(updates) > 0 {
				if updates[0].VehicleID == nil {
					log.Errorf("Missing vehicle ID from spoof file %s", f.Name())
					continue
				}
				vehicleID := *updates[0].VehicleID
				s.updates[vehicleID] = updates
				s.spoofIndexes[vehicleID] = 0
			}

			log.Debugf("Read %d updates from spoof file %s", len(updates), f.Name())
		}
	}
}

// Spoofs the next location for each vehicle
func (s *Spoofer) spoof() {
	for vehicleID, updates := range s.updates {
		update := updates[s.spoofIndexes[vehicleID]]
		update.Created = time.Now()
		update.Time = time.Now()
		update.ID = s.updateID
		// Create a spoofed location with this data and notify subscribers
		if err := s.ms.CreateLocation(&update); err != nil {
			log.WithError(err).Errorf("Could not create spoofed location for vehicle %d", vehicleID)
			return
		}
		log.Debugf("Spoofed location for vehicle %d", vehicleID)
		s.notifySubscribers(&update)

		// Update the next spoof index for this vehicle
		s.spoofIndexes[vehicleID] += 1
		if s.spoofIndexes[vehicleID] >= len(updates) {
			s.spoofIndexes[vehicleID] = 0
		}
		s.updateID += 1
	}
}

// Subscribe allows callers to provide a function that is called after Spoofer creates a new location.
// Automatically reroutes all of Updater's subscribers to Spoofer when update spoofing is enabled.
func (s *Spoofer) Subscribe(f func(*shuttletracker.Location)) {
	s.sm.Lock()
	s.subscribers = append(s.subscribers, f)
	s.sm.Unlock()
}

// Notifies all of Spoofer's subscribers with a new location.
func (s *Spoofer) notifySubscribers(loc *shuttletracker.Location) {
	s.sm.Lock()
	for _, sub := range s.subscribers {
		go sub(loc)
	}
	s.sm.Unlock()
}
