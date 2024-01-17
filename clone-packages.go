package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Argument struct {
	SpecFile string
	OutDir   string
}

func (a *Argument) Parse() {
	flag.StringVar(&a.SpecFile, "spec", "pubspec.yaml", "pubspec.yaml file")
	flag.StringVar(&a.OutDir, "out-dir", "output", "Output directory")
	flag.Parse()
}

type Package struct {
	Name         string // ex: easy_localization
	Version      string // ex: 3.0.3
	ArchiveUrl   string
	Info         string
	Dependencies []*Package
}

// Usage: ./pudev_get --spec=pubspec.yaml --out-dir=output
func main() {
	args := &Argument{}
	args.Parse()

	log.Printf("spec file %v output directory %v", args.SpecFile, args.OutDir)

	// read spec file
	packages := make([]*Package, 0)
	{
		file, err := os.Open(args.SpecFile)
		if err != nil {
			log.Fatal(err.Error())
			return
		}
		defer file.Close()
		fileData, err := io.ReadAll(file)
		if err != nil {
			log.Fatal(err.Error())
			return
		}
		spec := make(map[string]interface{})
		err = yaml.Unmarshal(fileData, spec)
		if err != nil {
			log.Fatal(err.Error())
			return
		}
		for k, v := range spec {
			if k == "dependencies" /*|| k == "dev_dependencies"*/ {
				if vMap, ok := v.(map[string]interface{}); ok {
					for k1, v1 := range vMap {
						if v1Map, ok := v1.(map[string]interface{}); ok {
							for k2, v2 := range v1Map {
								log.Printf("k1=%v k2=%v v2=%v", k1, k2, v2)
							}
						} else if v1String, ok := v1.(string); ok {
							log.Printf("k1=%v v1=%v", k1, v1String)
							v1String = strings.Trim(v1String, "^")
							packages = append(packages, &Package{
								Name:         k1,
								Version:      v1String,
								Dependencies: make([]*Package, 0),
							})
						}
					}
				}
			}
		}
	}
	log.Printf("%v packages in spec file:", len(packages))
	for _, p := range packages {
		log.Printf("  %v %v", p.Name, p.Version)
	}

	// get package info from pub.dev
	// https://pub.dev/api/packages/easy_localization

	for {
		newPackages := make([]*Package, 0)
		for idx, p := range packages {
			if len(p.ArchiveUrl) != 0 {
				continue
			}
			info, err := getPackageInfoFromPubDev(p.Name, p.Version)
			if err != nil {
				log.Fatal(err.Error())
				return
			}
			if len(info.ArchiveUrl) == 0 {
				log.Printf("cannot find %v %v", p.Name, p.Version)
				continue
			}
			packages[idx].Dependencies = info.Dependencies
			packages[idx].ArchiveUrl = info.ArchiveUrl
			packages[idx].Version = info.Version
			packages[idx].Info = info.Info

			for _, dep := range info.Dependencies {
				exists := false
				for _, p2 := range packages {
					if dep.Name == p2.Name && dep.Version == p2.Version {
						exists = true
						break
					}
				}
				for _, p2 := range newPackages {
					if dep.Name == p2.Name && dep.Version == p2.Version {
						exists = true
						break
					}
				}
				if !exists {
					newPackages = append(newPackages, &Package{
						Name:    dep.Name,
						Version: dep.Version,
					})
				}
			}
		}
		packages = append(packages, newPackages...)
		if len(newPackages) == 0 {
			break
		}
	}

	log.Printf("Total %v packages", len(packages))
	os.MkdirAll(args.OutDir, 0777)
	for _, p := range packages {
		log.Printf("%v %v %v %v", p.Name, p.Version, p.ArchiveUrl, len(p.Dependencies))
		// info
		{
			fileOutputPath := filepath.Join(args.OutDir, p.Name+"@info")
			if _, err := os.Stat(fileOutputPath); errors.Is(err, os.ErrNotExist) {
				file, err := os.Create(fileOutputPath)
				if err != nil {
					log.Fatal(err.Error())
					return
				}
				file.Write([]byte(p.Info))
				file.Close()
			}
		}
		// archive
		if len(p.ArchiveUrl) > 0 {
			fileName := filepath.Base(p.ArchiveUrl)
			fileOutputPath := filepath.Join(args.OutDir, p.Name+"@"+fileName)
			if _, err := os.Stat(fileOutputPath); errors.Is(err, os.ErrNotExist) {
				data, err := getApi(p.ArchiveUrl)
				if err != nil {
					log.Fatal(err.Error())
					return
				}
				file, err := os.Create(fileOutputPath)
				if err != nil {
					log.Fatal(err.Error())
					return
				}
				file.Write(data)
				file.Close()
			} else {
				log.Printf("  cached")
			}
		}
	}

	log.Printf("exit")
}

type PubDevPackageVersion struct {
	Version    string               `json:"Version"`
	Pubspec    PubDevPackagePubspec `json:"pubspec"`
	ArchiveUrl string               `json:"archive_url"`
}
type PubDevPackagePubspec struct {
	Name            string                 `json:"name"`
	Description     string                 `json:"description"`
	Version         string                 `json:"version"`
	Dependencies    map[string]interface{} `json:"dependencies"`
	DevDependencies map[string]interface{} `json:"dev_dependencies"`
}
type PubDevPackage struct {
	Name     string                 `json:"name"`
	Latest   PubDevPackageVersion   `json:"latest"`
	Versions []PubDevPackageVersion `json:"versions"`
}

func getPackageInfoFromPubDev(pkgName string, pkgVer string) (*Package, error) {
	log.Printf("read info package %v version %v", pkgName, pkgVer)

	url := fmt.Sprintf("https://pub.dev/api/packages/%v", pkgName)
	data, err := getApi(url)
	if err != nil {
		return nil, err
	}

	pubDevPackage := &PubDevPackage{}
	if err = json.Unmarshal(data, pubDevPackage); err != nil {
		return nil, err
	}

	result := &Package{
		Name:         pkgName,
		Version:      pkgVer,
		Info:         string(data),
		Dependencies: make([]*Package, 0),
	}
	for _, ver := range pubDevPackage.Versions {
		if ver.Version == pkgVer {
			result.ArchiveUrl = ver.ArchiveUrl
			for k1, v1 := range ver.Pubspec.Dependencies {
				if vString, ok := v1.(string); ok {
					vString = strings.Trim(vString, "^")
					if idx := strings.LastIndex(vString, "<="); idx != -1 {
						vString = vString[idx+2:]
					} else if idx := strings.LastIndex(vString, "<"); idx != -1 {
						vString = vString[idx+1:]
					}
					result.Dependencies = append(result.Dependencies, &Package{
						Name:    k1,
						Version: vString,
					})
					log.Printf("  dependency %v %v", k1, vString)

				}
			}
			/*for k1, v1 := range ver.Pubspec.DevDependencies {
				if vString, ok := v1.(string); ok {
					vString = strings.Trim(vString, "^")
					if idx := strings.LastIndex(vString, "<="); idx != -1 {
						vString = vString[idx+2:]
					} else if idx := strings.LastIndex(vString, "<"); idx != -1 {
						vString = vString[idx+1:]
					}
					result.Dependencies = append(result.Dependencies, &Package{
						Name:    k1,
						Version: vString,
					})
					log.Printf("  dependency %v %v", k1, vString)

				}
			}*/
			break
		}
	}
	if len(result.ArchiveUrl) == 0 {
		result.Version = pubDevPackage.Latest.Version
		result.ArchiveUrl = pubDevPackage.Latest.ArchiveUrl
		for k1, v1 := range pubDevPackage.Latest.Pubspec.Dependencies {
			if vString, ok := v1.(string); ok {
				vString = strings.Trim(vString, "^")
				if idx := strings.LastIndex(vString, "<="); idx != -1 {
					vString = vString[idx+2:]
				} else if idx := strings.LastIndex(vString, "<"); idx != -1 {
					vString = vString[idx+1:]
				}
				result.Dependencies = append(result.Dependencies, &Package{
					Name:    k1,
					Version: vString,
				})
				log.Printf("  dependency %v %v", k1, vString)

			}
		}
	}
	return result, nil
}

func getApi(url string) ([]byte, error) {
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return responseBody, nil
}
