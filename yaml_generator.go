package yamlgen

import (
	"errors"
	"fmt"
	"github.com/we7coreteam/gorm-gen-yaml/template"
	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TableGenStatus 表生成状态
type TableGenStatus uint

const (
	TableGenStatusNone    TableGenStatus = 0 // 未生成
	TableGenStatusPending TableGenStatus = 1 // 生成中
	TableGenStatusDone    TableGenStatus = 2 // 已完成
)

type GeneratorTable struct {
	Status    TableGenStatus
	ModelName string
}

type YamlGenerator struct {
	yaml                *DbTable
	gen                 *gen.Generator
	generatedTable      map[string]*GeneratorTable
	columnOptionSaveDir string
}

func NewYamlGenerator(path string) *YamlGenerator {
	obj := &YamlGenerator{}
	err := obj.loadFromFile(path)
	if err != nil {
		panic(err)
	}
	obj.generatedTable = make(map[string]*GeneratorTable)
	return obj
}

type DbTable struct {
	Config         Config  `yaml:"config"`
	Table          []Table `yaml:"relation"`
	TableMap       map[string]*Table
	RelateTableMap map[string]struct{}
}

type Table struct {
	Name   string            `yaml:"table"`
	Relate []Relate          `yaml:"relate"`
	Column map[string]Column `yaml:"column"`
	Props  map[string]Column `yaml:"props"`
}

type Column struct {
	Type       string                       `yaml:"type"`
	Serializer string                       `yaml:"serializer"`
	Tag        map[string]map[string]string `yaml:"tag"`
	Comment    string                       `yaml:"comment"`
	Rename     string                       `yaml:"rename"`
	Json       string                       `yaml:"json"`
}

type Relate struct {
	Table          string `yaml:"table"`
	ForeignKey     string `yaml:"foreign_key"`
	References     string `yaml:"references"`
	JoinForeignKey string `yaml:"join_foreign_key"`
	JoinReferences string `yaml:"join_references"`
	Many2many      string `yaml:"many_2_many"`
	Type           string `yaml:"type"`
	JSONTag        string `yaml:"json"`
}

type Config struct {
	TagJsonCamel string `yaml:"tag_json_camel"`
}

func (y *YamlGenerator) UseGormGenerator(g *gen.Generator) *YamlGenerator {
	y.gen = g

	y.SetColumnOptionSaveDir(g.Config.OutPath + "/../accessor")

	return y
}

func (y *YamlGenerator) loadFromFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s file not found: %w", path, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", path, err)
	}

	y.yaml = &DbTable{}
	err = yaml.Unmarshal(content, y.yaml)
	if err != nil {
		return err
	}
	y.yaml.TableMap = make(map[string]*Table)
	y.yaml.RelateTableMap = make(map[string]struct{})

	for _, table := range y.yaml.Table {
		t := table
		y.yaml.TableMap[table.Name] = &t

		for _, relateTable := range table.Relate {
			y.yaml.RelateTableMap[relateTable.Table] = struct{}{}
		}
	}

	return nil
}

func (y *YamlGenerator) SetColumnOptionSaveDir(columnOptionSaveDir string) {
	y.columnOptionSaveDir = columnOptionSaveDir

	if err := os.MkdirAll(y.columnOptionSaveDir, os.ModePerm); err != nil {
		panic(err)
	}
}

func (y *YamlGenerator) generateColumnOption(column Column) error {
	var columnOptionTemplate string
	var exists bool

	if column.Serializer == "json" || column.Serializer == "gob" || column.Serializer == "unixtime" {
		columnOptionTemplate, exists = template.ColumnOptionTemplate["json"]
	} else {
		columnOptionTemplate, exists = template.ColumnOptionTemplate["common"]
	}

	if !exists {
		return errors.New("serializer type not support")
	}
	columnOptionTemplate = strings.Replace(columnOptionTemplate, "{{Package}}", filepath.Base(y.columnOptionSaveDir), 1)
	columnOptionTemplate = strings.Replace(columnOptionTemplate, "{{OptionStructName}}", column.Type, -1)

	p := filepath.Join(y.columnOptionSaveDir, CamelCaseToUnderscore(column.Type)+".go")
	_, err := os.Stat(p)
	if os.IsNotExist(err) {
		return os.WriteFile(p, []byte(columnOptionTemplate), 0640)
	}
	return nil
}

func (y *YamlGenerator) getTableRelateOpt(table *Table) []gen.ModelOpt {
	opt := make([]gen.ModelOpt, len(table.Relate))
	for i, relate := range table.Relate {
		relatePointer := false
		var fieldType field.RelationshipType
		switch relate.Type {
		case "has_one":
			fieldType = field.HasOne
			relatePointer = true
		case "has_many":
			fieldType = field.HasMany
		case "many_many":
			fieldType = field.Many2Many
		case "belongs_to":
			fieldType = field.BelongsTo
			relatePointer = true
		}
		relateConfig, jsonTag := y.buildRelateConfig(relate)

		generatedTable, exists := y.generatedTable[relate.Table]
		if !exists {
			panic(fmt.Sprintf("table %s not generated yet, cannot create relation", relate.Table))
		}

		queryStructMeta, ok := y.gen.Data[generatedTable.ModelName]
		if !ok || queryStructMeta.QueryStructMeta == nil {
			panic(fmt.Sprintf("QueryStructMeta for table %s not found", relate.Table))
		}

		opt[i] = gen.FieldRelate(fieldType, generatedTable.ModelName, queryStructMeta.QueryStructMeta, &field.RelateConfig{
			GORMTag:       relateConfig,
			RelatePointer: relatePointer,
			JSONTag:       jsonTag,
		})
	}

	return opt
}

// buildRelateConfig 构建关联配置
func (y *YamlGenerator) buildRelateConfig(relate Relate) (field.GormTag, string) {
	relateConfig := make(field.GormTag)

	type tagMapping struct {
		yamlKey string
		gormKey string
	}

	mappings := []tagMapping{
		{relate.ForeignKey, "foreignKey"},
		{relate.JoinForeignKey, "joinForeignKey"},
		{relate.References, "references"},
		{relate.JoinReferences, "joinReferences"},
		{relate.Many2many, "many2many"},
	}

	for _, mapping := range mappings {
		if mapping.yamlKey != "" {
			relateConfig.Append(mapping.gormKey, mapping.yamlKey)
		}
	}

	// 确保 JSONTag 始终包含 omitempty
	jsonTag := relate.JSONTag
	if jsonTag == "" {
		jsonTag = NamingConversion(relate.Table, y.yaml.Config.TagJsonCamel)
	}
	// 如果没有 omitempty，自动添加
	if !strings.Contains(jsonTag, "omitempty") {
		jsonTag = jsonTag + ",omitempty"
	}

	return relateConfig, jsonTag
}

func (y *YamlGenerator) getTableColumnOpt(table *Table) ([]gen.ModelOpt, bool) {
	opt := make([]gen.ModelOpt, 0)
	//找到column生成自定义column类型
	hasOption := false
	for name, column := range table.Column {
		if column.Type != "" {
			if column.Serializer == "json" || column.Serializer == "gob" || column.Serializer == "unixtime" {
				if column.Tag == nil {
					column.Tag = map[string]map[string]string{
						"gorm": {
							"serializer": column.Serializer,
						},
					}
				} else {
					column.Tag["gorm"]["serializer"] = column.Serializer
				}
			}

			if strings.Contains(strings.ToLower(column.Type), "option") {
				// 生成对应的类型文件
				err := y.generateColumnOption(column)
				if err != nil {
					panic(err)
				}

				hasOption = true
				opt = append(opt, gen.FieldType(name, "*"+filepath.Base(y.columnOptionSaveDir)+"."+column.Type))
			} else {
				opt = append(opt, gen.FieldType(name, column.Type))
			}
		}
		if column.Json != "" {
			opt = append(opt, gen.FieldJSONTag(name, column.Json))
		}

		if column.Tag != nil {
			for tagType, tags := range column.Tag {
				ttags := tags
				if tagType == "gorm" {
					opt = append(opt, gen.FieldGORMTag(name, func(tag field.GormTag) field.GormTag {
						for tagName, val := range ttags {
							tag = tag.Set(tagName, val)
						}
						return tag
					}))
				} else {
					tTagType := tagType
					opt = append(opt, gen.FieldTag(name, func(tag field.Tag) field.Tag {
						tagStr := ""
						for tagName, val := range ttags {
							if val == "" {
								tagStr += tagName + ";"
							} else {
								tagStr += tagName + ":" + val + ";"
							}
						}
						return tag.Set(tTagType, strings.TrimRight(tagStr, ";"))
					}))
				}
			}
		}
		if column.Rename != "" {
			opt = append(opt, gen.FieldRename(name, column.Rename))
		}
		if column.Comment != "" {
			opt = append(opt, gen.FieldComment(name, column.Comment))
		}

	}

	for name, column := range table.Props {
		tag := field.Tag{}
		if column.Json != "" {
			tag.Set(field.TagKeyJson, column.Json)
		} else {
			tag.Set(field.TagKeyJson, NamingConversion(name, y.yaml.Config.TagJsonCamel)+",omitempty")
		}

		tag.Set(field.TagKeyGorm, "-")

		columnType := column.Type
		if strings.Contains(strings.ToLower(column.Type), "option") {
			// 自定义生成 Scan Value
			column.Serializer = "common"
			err := y.generateColumnOption(column)
			if err != nil {
				panic(err)
			}
			hasOption = true
			columnType = "*" + filepath.Base(y.columnOptionSaveDir) + "." + column.Type
		}

		opt = append(opt, gen.FieldNew(UnderscoreToCamelCase(name, true), columnType, tag))

	}
	return opt, hasOption
}

// generateSimpleTable 生成简单表（无自定义列配置）
func (y *YamlGenerator) generateSimpleTable(tableName string, status TableGenStatus, opt ...gen.ModelOpt) {
	relateMate := y.gen.GenerateModel(tableName, opt...)
	y.gen.ApplyBasic(relateMate)
	y.generatedTable[tableName] = &GeneratorTable{
		Status:    status,
		ModelName: relateMate.ModelStructName,
	}
}

// applyModelWithPackage 生成模型并处理包导入
func (y *YamlGenerator) applyModelWithPackage(tableName string, tableOpt []gen.ModelOpt, hasOption bool) {
	relateMate := y.gen.GenerateModel(tableName, tableOpt...)
	if hasOption {
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.NeedName,
			Dir:  y.columnOptionSaveDir,
		})
		if err != nil {
			panic(err)
		}
		relateMate.ImportPkgPaths = append(relateMate.ImportPkgPaths, "\""+pkgs[0].PkgPath+"\"")
	}

	// 删除旧数据，应用新数据
	if generatedTable, exists := y.generatedTable[tableName]; exists {
		delete(y.gen.Data, generatedTable.ModelName)
	}
	y.gen.ApplyBasic(relateMate)
	y.generatedTable[tableName] = &GeneratorTable{
		Status:    TableGenStatusDone,
		ModelName: relateMate.ModelStructName,
	}
}

func (y *YamlGenerator) generateFromTable(table *Table, opt ...gen.ModelOpt) {
	// 检查是否已经生成过，避免重复处理
	if _, exists := y.generatedTable[table.Name]; exists {
		return
	}

	// 提前标记为生成中，防止循环依赖
	y.generatedTable[table.Name] = &GeneratorTable{
		Status:    TableGenStatusPending,
		ModelName: "",
	}

	// 先递归生成所有关联表（不处理关联字段）
	for _, relate := range table.Relate {
		_, isInRelateTable := y.yaml.RelateTableMap[relate.Table]

		if !isInRelateTable {
			// 递归生成不在 RelateTableMap 中的表
			if _, exists := y.generatedTable[relate.Table]; !exists {
				y.generateFromTable(y.yaml.TableMap[relate.Table], opt...)
			}
		} else {
			// 直接生成在 RelateTableMap 中的表
			if _, exists := y.generatedTable[relate.Table]; exists {
				continue
			}
			status := TableGenStatusPending
			_, isInTable := y.yaml.TableMap[relate.Table]
			if !isInTable {
				status = TableGenStatusDone
			}
			y.generateSimpleTable(relate.Table, status, opt...)
		}
	}

	// 生成当前表（不包含关联字段）
	columnOpt, hasOption := y.getTableColumnOpt(table)
	tableOpt := make([]gen.ModelOpt, 0, len(opt)+len(columnOpt))
	tableOpt = append(tableOpt, opt...)
	tableOpt = append(tableOpt, columnOpt...)

	y.applyModelWithPackage(table.Name, tableOpt, hasOption)
}

// addRelationFields 为所有表添加关联字段（第二阶段）
func (y *YamlGenerator) addRelationFields(opt ...gen.ModelOpt) {
	// 遍历所有有 Relate 的表，重新生成并添加关联字段
	// 由于第一阶段已生成所有表的基础结构，这里只需一次遍历即可
	for _, table := range y.yaml.Table {
		t := y.yaml.TableMap[table.Name]
		if t == nil || len(t.Relate) == 0 {
			continue
		}

		y.regenerateTableWithRelations(t, opt...)
	}
}

// regenerateTableWithRelations 重新生成表，添加关联字段
func (y *YamlGenerator) regenerateTableWithRelations(table *Table, opt ...gen.ModelOpt) {
	// 获取关联字段选项
	relateOpt := y.getTableRelateOpt(table)
	if len(relateOpt) == 0 {
		return
	}

	// 重新生成模型，添加关联字段
	columnOpt, hasOption := y.getTableColumnOpt(table)
	tableOpt := make([]gen.ModelOpt, 0, len(opt)+len(relateOpt)+len(columnOpt))
	tableOpt = append(tableOpt, opt...)
	tableOpt = append(tableOpt, relateOpt...)
	tableOpt = append(tableOpt, columnOpt...)

	y.applyModelWithPackage(table.Name, tableOpt, hasOption)
}

func (y *YamlGenerator) Generate(opt ...gen.ModelOpt) {
	if y.yaml.Config.TagJsonCamel != "" {
		y.gen.WithJSONTagNameStrategy(func(columnName string) (tagContent string) {
			return NamingConversion(columnName, y.yaml.Config.TagJsonCamel)
		})
	}

	// 第一阶段：生成所有表的基础结构（不包含关联字段）
	for _, table := range y.yaml.Table {
		y.generateFromTable(y.yaml.TableMap[table.Name], opt...)
	}

	// 第二阶段：为所有表添加关联字段
	y.addRelationFields(opt...)
}
