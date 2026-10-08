package main

// Расширенный аудит по методике настройки MS SQL Server под 1С
// (по материалам Habr: «Настройка MS SQL Server под 1С: планирование и развёртывание»,
// части 1–2, Maxpiter). Проверки, которые можно выполнить автоматически,
// дополняются чек-листом того, что нужно проверить руками на уровне ОС/гипервизора/сети.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	guidHighPerformance = "8C5E7FDA-E8BF-4A96-9A85-A6E23A8C635C"
	guidBalanced        = "381B4222-F694-41F0-9685-FF5BB260DF2E"
	guidPowerSaver      = "A1841308-3541-4FAB-BC81-F71556F20B4A"
	collation1C         = "Cyrillic_General_CI_AS"
)

// isSystemDB — служебные базы, которые не участвуют в «рабочих» проверках.
func isSystemDB(name string) bool {
	switch name {
	case "master", "model", "msdb", "tempdb":
		return true
	}
	return false
}

// userDatabases — рабочие (не системные, не offline) базы снимка.
func userDatabases(snap *Snapshot) []map[string]any {
	out := []map[string]any{}
	for _, d := range snap.Databases {
		name := sstr(d["name"])
		if isSystemDB(name) || sstr(d["state_desc"]) != "ONLINE" {
			continue
		}
		out = append(out, d)
	}
	return out
}

// dbSizeMB — суммарный размер файлов данных базы по sys.master_files.
func dbSizeMB(snap *Snapshot, dbName string) float64 {
	total := 0.0
	for _, f := range snap.FileCfg {
		if sstr(f["db_name"]) == dbName {
			total += toF(f["size_mb"])
		}
	}
	return total
}

// rcsiState — базы без READ_COMMITTED_SNAPSHOT и базы со «старым» уровнем совместимости
// (признак легаси-конфигураций 1С, где RCSI не помогает: транзакции идут на Repeatable Read/Serializable).
func rcsiState(snap *Snapshot) (off []string, legacy []string) {
	for _, d := range userDatabases(snap) {
		name := sstr(d["name"])
		if !bval(d["is_read_committed_snapshot_on"]) {
			off = append(off, name)
		}
		if cl := toI(d["compatibility_level"]); cl > 0 && cl <= 110 {
			legacy = append(legacy, fmt.Sprintf("%s (compat %d)", name, cl))
		}
	}
	return off, legacy
}

// volKey выделяет том (диск) из пути к файлу: "D:" для Windows-путей, иначе — ведущий каталог/UNC.
func volKey(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if len(p) >= 2 && p[1] == ':' {
		return strings.ToUpper(p[:2])
	}
	if strings.HasPrefix(p, `\\`) {
		parts := strings.Split(strings.TrimPrefix(p, `\\`), `\`)
		if len(parts) >= 2 {
			return `\\` + parts[0] + `\` + parts[1]
		}
		return p
	}
	if i := strings.IndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return p
}

// extendedAudit добавляет проверки по методике настройки (диски, ОС, безопасность,
// мониторинг, режимы баз) и заполняет чек-лист отчёта.
func extendedAudit(snap *Snapshot, an *Analysis, add addFn) {
	audit := &an.ConfigAudit
	cl := &an.Checklist

	chk := func(title, status, note string) {
		*cl = append(*cl, ChecklistRow{Title: title, Status: status, Note: note})
	}

	// ==================================================================
	//  1. Коллация экземпляра и баз (для 1С нужна Cyrillic_General_CI_AS)
	// ==================================================================
	col := strings.TrimSpace(snap.Instance["collation"])
	if col != "" {
		if strings.EqualFold(col, collation1C) {
			*audit = append(*audit, AuditRow{"Коллация экземпляра", col, collation1C, "ok"})
			chk("Коллация экземпляра — "+collation1C, "ok", "Коллация задаётся при установке; для баз 1С требуется "+collation1C+".")
		} else {
			*audit = append(*audit, AuditRow{"Коллация экземпляра", col, collation1C, "warn"})
			chk("Коллация экземпляра — "+collation1C, "warn", "Текущая коллация: "+col+". Для 1С нужна "+collation1C+".")
			add("warning", "1С", "Коллация экземпляра отличается от рекомендуемой для 1С",
				"Коллация экземпляра — "+col+", а для баз 1С требуется "+collation1C+". "+
					"Коллация задаётся при установке инстанса и наследуется всеми базами; ошибка обычно "+
					"обнаруживается только при создании первой базы 1С.",
				"Спланируйте смену коллации: как правило, это переустановка инстанса/миграция баз. "+
					"До этого создавать базы 1С на этом экземпляре нельзя. Если сервер не используется под 1С — "+
					"проверка неактуальна.", "")
		}
	}
	var dbColl []string
	for _, d := range userDatabases(snap) {
		if c := strings.TrimSpace(sstr(d["collation_name"])); c != "" && col != "" && !strings.EqualFold(c, col) {
			dbColl = append(dbColl, fmt.Sprintf("%s (%s)", sstr(d["name"]), c))
		}
	}
	if len(dbColl) > 0 {
		add("info", "1С", "Коллация баз отличается от коллации экземпляра",
			"Базы: "+strings.Join(dbColl, ", ")+". Разные коллации затрудняют JOIN'ы между базами и "+
				"обработку строк (сравнения и сортировки не используют индексы).",
			"Приведите коллацию баз к коллации экземпляра (или сознательно задокументируйте исключения).", "")
	}

	// ==================================================================
	//  2. Память: max server memory и резерв для ОС
	// ==================================================================
	if ramMB := toF(snap.SysInfo["physical_memory_kb"]) / 1024; ramMB > 0 {
		reserveMB := math.Max(4096, ramMB*0.15)
		if ramMB >= 65536 {
			reserveMB = math.Max(reserveMB, 8192) // на крупных машинах резерв ОС растёт
		}
		if maxMem := maxServerMemoryMB(snap); maxMem > 0 {
			osReserve := ramMB - float64(maxMem)
			st := "ok"
			if osReserve < reserveMB*0.5 {
				st = "bad"
			} else if osReserve < reserveMB {
				st = "warn"
			}
			*audit = append(*audit, AuditRow{"Резерв памяти для ОС", fmt.Sprintf("%.0f ГБ", osReserve/1024),
				fmt.Sprintf("не менее %.0f ГБ", reserveMB/1024), st})
			if st != "ok" {
				add("warning", "Память", "Недостаточный резерв памяти для операционной системы",
					fmt.Sprintf("SQL Server ограничен значением %d МБ, ОС остаётся %.0f ГБ. "+
						"Службы, потоки и сторонние процессы (сервер приложений 1С, антивирус, агент) "+
						"работают за счёт этого резерва; при его нехватке начинается подкачка и деградация всей машины.",
						maxMem, osReserve/1024),
					fmt.Sprintf("Уменьшите 'max server memory' до ≈ %d МБ (ОЗУ минус %.0f ГБ).",
						int(ramMB-reserveMB), reserveMB/1024),
					fmt.Sprintf("EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
						"EXEC sys.sp_configure 'max server memory (MB)', %d;\nRECONFIGURE;", int(ramMB-reserveMB)))
			}
		}
	}

	// ==================================================================
	//  3. Службы: Instant File Initialization, учётные записи
	// ==================================================================
	var noIFI, localAcct, unusualSvc []string
	for _, s := range snap.Services {
		name := sstr(s["servicename"])
		acct := sstr(s["service_account"])
		up := strings.ToUpper(name)
		if strings.Contains(up, "SQL SERVER") && !strings.Contains(up, "AGENT") {
			v := strings.ToUpper(strings.TrimSpace(sstr(s["instant_file_initialization_enabled"])))
			if v == "NO" || v == "FALSE" || v == "0" || v == "DISABLED" {
				noIFI = append(noIFI, name)
			}
			if v == "" {
				chk("Instant File Initialization", "manual",
					"Колонка instant_file_initialization_enabled недоступна (SQL Server старше 2016 SP1) — проверьте право вручную.")
			}
		}
		if acct != "" {
			u := strings.ToUpper(acct)
			if strings.Contains(u, "NT AUTHORITY") || strings.HasPrefix(u, "NT SERVICE") ||
				strings.Contains(u, "LOCAL SYSTEM") || strings.Contains(u, "NETWORK SERVICE") {
				localAcct = append(localAcct, name+" — "+acct)
			}
		}
		low := strings.ToUpper(name)
		if strings.Contains(low, "ANALYSIS") || strings.Contains(low, "REPORTING") ||
			strings.Contains(low, "INTEGRATION") {
			unusualSvc = append(unusualSvc, name)
		}
	}
	if len(noIFI) > 0 {
		*audit = append(*audit, AuditRow{"Instant File Initialization", "отключён", "включён", "warn"})
		chk("Instant File Initialization", "warn", "Файлы данных растут и восстанавливаются с предварительным обнулением — медленнее.")
		add("warning", "Конфигурация ОС", "Instant File Initialization отключён",
			"Службы: "+strings.Join(noIFI, ", ")+". Без этого права файлы данных растут и восстанавливаются "+
				"из копии с предварительным заполнением нулями — на больших базах это часы лишнего времени.",
			"Выдайте служебной учётной записи SQL Server право «Perform Volume Maintenance Tasks» "+
				"(secpol.msc → Локальные политики → Назначение прав пользователя) и перезапустите службу. "+
				"Журналы транзакций это право не ускоряет (они всегда зануляются).", "")
	} else if len(snap.Services) > 0 {
		*audit = append(*audit, AuditRow{"Instant File Initialization", "включён", "включён", "ok"})
		chk("Instant File Initialization", "ok", "Файлы данных растут без предварительного обнуления.")
	}
	if len(localAcct) > 0 {
		*audit = append(*audit, AuditRow{"Учётные записи служб", "локальные", "доменные/gMSA", "warn"})
		chk("Учётные записи служб", "warn", "Локальные учётки сложнее ротировать и хуже подходят для сетевых ресурсов.")
		add("info", "Безопасность", "Службы SQL Server работают под локальными учётными записями",
			strings.Join(localAcct, "; ")+". По методике службы сервера СУБД и сервера приложений 1С "+
				"работают под доменными учётными записями; вершина схемы — групповые управляемые учётные "+
				"записи служб (gMSA): пароль генерирует и ротирует домен, его не знает ни один сотрудник.",
			"Переведите службы на доменные учётные записи, подключение к СУБД — на Windows-аутентификацию, "+
				"права выдавайте под задачу (вместо sysadmin «чтобы работало»).", "")
	}
	if len(unusualSvc) > 0 {
		add("info", "Конфигурация ОС", "Установлены дополнительные компоненты SQL Server",
			"Службы: "+strings.Join(unusualSvc, ", ")+". SSAS/SSRS/SSIS занимают память продуктивного сервера "+
				"без пользы для 1С (если не используются по назначению).",
			"Удалите неиспользуемые компоненты: на прод ставится только ядро СУБД.", "")
	} else if len(snap.Services) > 0 {
		chk("Состав компонентов SQL Server", "ok", "Дополнительные компоненты (SSAS/SSRS/SSIS) не обнаружены.")
	}

	// ==================================================================
	//  4. План электропитания (Windows + BIOS/хост виртуализации)
	// ==================================================================
	if pp := snap.NetConfig["power_plan"]; pp != "" {
		switch pp {
		case guidHighPerformance:
			*audit = append(*audit, AuditRow{"План электропитания", "Высокая производительность", "High performance", "ok"})
			chk("План электропитания ОС", "ok", "Windows работает на плане «Высокая производительность».")
		case guidBalanced:
			*audit = append(*audit, AuditRow{"План электропитания", "Сбалансированный", "High performance", "warn"})
			chk("План электропитания ОС", "warn", "Сбалансированный план снижает частоты процессора — сервер работает вполсилы.")
			add("warning", "Конфигурация ОС", "План электропитания Windows — «Сбалансированный»",
				"На этом плане Windows снижает частоты процессора; сервер СУБД работает вполсилы, "+
					"при этом в логах SQL Server нет ни одного признака проблемы (см. KB2207548).",
				"Выставьте план «Высокая производительность» и проверьте настройки энергосбережения в BIOS. "+
					"На виртуальной машине план выставляется и внутри гостя, и на хосте (частотами управляет железо под гипервизором).", "")
		case guidPowerSaver:
			*audit = append(*audit, AuditRow{"План электропитания", "Экономия энергии", "High performance", "bad"})
			chk("План электропитания ОС", "warn", "План «Экономия энергии» — наихудший вариант для СУБД.")
			add("warning", "Конфигурация ОС", "План электропитания Windows — «Экономия энергии»",
				"План экономии энергии ограничивает частоты процессора сильнее всех — сервер работает вполсилы.",
				"Выставьте «Высокая производительность» в Windows, BIOS и (для ВМ) на хосте гипервизора.", "")
		default:
			*audit = append(*audit, AuditRow{"План электропитания", pp, "High performance", "warn"})
			chk("План электропитания ОС", "manual", "Схема электропитания не распознана ("+pp+") — проверьте вручную.")
		}
	} else {
		chk("План электропитания ОС", "manual",
			"Не удалось прочитать схему электропитания (нужны права на чтение реестра). Проверьте вручную: должна быть «Высокая производительность».")
	}

	// ==================================================================
	//  5. Виртуализация
	// ==================================================================
	vmDesc := strings.ToUpper(sstr(snap.SysInfo["virtual_machine_type_desc"]))
	if _, ok := snap.SysInfo["virtual_machine_type_desc"]; ok && vmDesc != "" && vmDesc != "NONE" {
		sockets := toI(snap.SysInfo["socket_count"])
		cores := toI(snap.SysInfo["cores_per_socket"])
		nodes := toI(snap.SysInfo["numa_node_count"])
		add("info", "Виртуализация", "Сервер — виртуальная машина: проверьте настройки гипервизора",
			fmt.Sprintf("Тип: %s. Сокетов: %d, ядер на сокет: %d, NUMA-узлов: %d. Изнутри гостя половина "+
				"проблем не видна: диспетчер задач показывает свободный процессор, а запросы выполняются медленно.",
				sstr(snap.SysInfo["virtual_machine_type_desc"]), sockets, cores, nodes),
			"Память продуктивной ВМ резервируется в полном объёме (VMware memory reservation; в Hyper-V — "+
				"отключить Dynamic Memory и задать статический объём): ballooning вытесняет буферный пул, "+
				"PLE обваливается без видимых причин. Переподписка vCPU держится близко к 1:1, "+
				"CPU Ready ≤ 5%; ядра «про запас» не раздаются — 8 vCPU нередко работают быстрее 24. "+
				"Проверьте vNUMA (в vSphere до 8.0 горячее добавление CPU отключает vNUMA), "+
				"паравиртуальный контроллер PVSCSI и раздельные виртуальные контроллеры для данных, "+
				"журналов и tempdb. Снапшоты долго не хранить (растут и замедляют запись; stun при "+
				"создании/удалении даёт регулярные подвисания по расписанию бэкапа). "+
				"Датастор не должен быть общим с соседними ВМ, отбирающими IOPS.", "")
		chk("Настройки гипервизора", "manual",
			"ВМ: проверьте memory reservation, CPU Ready, vNUMA, PVSCSI, отсутствие долгих снапшотов и соседей по датастору.")
	} else if vmDesc != "" {
		chk("Виртуализация", "ok", "Физический сервер (или тип ВМ не определён): проверьте план питания BIOS и DiskSpd-эталон.")
	}

	// ==================================================================
	//  6. Дисковая подсистема: разнесение данных, журналов, tempdb
	// ==================================================================
	type volRoles struct {
		data, log, tempdb bool
		files             []string
	}
	vols := map[string]*volRoles{}
	roleOf := func(f map[string]any) string {
		db := sstr(f["db_name"])
		if db == "tempdb" {
			return "tempdb"
		}
		if strings.EqualFold(sstr(f["type_desc"]), "LOG") {
			return "log"
		}
		return "data"
	}
	for _, f := range snap.FileCfg {
		v := volKey(sstr(f["physical_name"]))
		if v == "" {
			continue
		}
		r := vols[v]
		if r == nil {
			r = &volRoles{}
			vols[v] = r
		}
		switch roleOf(f) {
		case "tempdb":
			r.tempdb = true
		case "log":
			r.log = true
		default:
			r.data = true
		}
		r.files = append(r.files, sstr(f["file_name"]))
	}
	var together []string
	for v, r := range vols {
		n := 0
		for _, b := range []bool{r.data, r.log, r.tempdb} {
			if b {
				n++
			}
		}
		if n >= 2 {
			var roles []string
			if r.data {
				roles = append(roles, "данные")
			}
			if r.log {
				roles = append(roles, "журналы")
			}
			if r.tempdb {
				roles = append(roles, "tempdb")
			}
			together = append(together, fmt.Sprintf("%s (%s)", v, strings.Join(roles, " + ")))
		}
	}
	sort.Strings(together)
	if len(vols) == 1 && len(together) > 0 {
		*audit = append(*audit, AuditRow{"Размещение файлов", "один том", "данные/журналы/tempdb — на разных томах", "bad"})
		chk("Разнесение файлов по томам", "warn", "Всё лежит на одном томе — потоки ввода-вывода встают в общую очередь.")
		add("warning", "Диски и файлы", "Данные, журналы и tempdb размещены на одном томе",
			"Файлы: "+strings.Join(together, "; ")+". Данные читаются случайным доступом, журнал пишется "+
				"последовательно, tempdb принимает шквал мелких операций — сведённые на один том потоки "+
				"выстраиваются в общую очередь и замедляют друг друга (рекомендация Microsoft по настройке ОС под SQL Server).",
			"Минимальная схема: C — ОС, D — данные, E — журналы, F — tempdb (отдельные LUN/RAID-массивы, "+
				"при встроенном контроллере — отдельные каналы). Тома под данные/журналы/tempdb форматируются "+
				"кластером 64 КБ и собираются на NVMe-накопителях, оптимизированных под запись (write intensive). "+
				"Резервные копии — на сетевой ресурс, не на сервер.", "")
	} else if len(together) > 0 {
		*audit = append(*audit, AuditRow{"Размещение файлов", strings.Join(together, "; "),
			"данные/журналы/tempdb — на разных томах", "warn"})
		chk("Разнесение файлов по томам", "warn", "Не все потоки разнесены: "+strings.Join(together, "; "))
		add("warning", "Диски и файлы", "Часть файлов с разным профилем нагрузки на одном томе",
			addJoin(together)+". Соседство данных с журналом или tempdb создаёт конкуренцию за диск "+
				"и добавляет задержки на ровном месте.",
			"Разнесите файлы данных, журналов транзакций и tempdb по отдельным томам/каналам; "+
				"кластер томов — 64 КБ, накопители — класса write intensive.", "")
	} else {
		chk("Разнесение файлов по томам", "ok", "Файлы данных, журналов и tempdb разнесены по разным томам.")
	}

	// Свободное место на томах (журнал и tempdb умеют съесть том целиком).
	var lowFree, critFree []string
	for _, v := range snap.Volumes {
		total, free := toF(v["total_gb"]), toF(v["free_gb"])
		if total <= 0 {
			continue
		}
		pct := free / total * 100
		mp := sstr(v["volume_mount_point"])
		if pct < 5 || free < 5 {
			critFree = append(critFree, fmt.Sprintf("%s — %.0f ГБ (%.1f%%)", mp, free, pct))
		} else if pct < 15 {
			lowFree = append(lowFree, fmt.Sprintf("%s — %.0f ГБ (%.1f%%)", mp, free, pct))
		}
	}
	if len(critFree) > 0 {
		*audit = append(*audit, AuditRow{"Свободное место на томах", strings.Join(critFree, "; "), "≥ 15%", "bad"})
		add("critical", "Диски и файлы", "Критически мало свободного места на томах с файлами баз",
			strings.Join(critFree, "; ")+". Журнал транзакций и tempdb растут скачками (версионирование "+
				"строк, длинные транзакции) — том заполнится и работа встанет.",
			"Освободите место, преаллоцируйте файлы с запасом, вынесите резервные копии на другой ресурс; "+
				"проверьте autogrowth файлов и рост журнала (log_reuse_wait).", "")
	} else if len(lowFree) > 0 {
		*audit = append(*audit, AuditRow{"Свободное место на томах", strings.Join(lowFree, "; "), "≥ 15%", "warn"})
		add("warning", "Диски и файлы", "Мало свободного места на томах с файлами баз",
			strings.Join(lowFree, "; ")+".",
			"Запланируйте расширение томов; храните резервные копии вне сервера, следите за ростом журнала.", "")
	} else if len(snap.Volumes) > 0 {
		chk("Свободное место на томах", "ok", "На всех томах с файлами баз достаточно свободного места.")
	}

	// ==================================================================
	//  7. tempdb: число файлов, равный размер, авторост
	// ==================================================================
	if len(snap.TempdbDetail) >= 1 {
		var sizes, growths []string
		min, max := math.MaxFloat64, 0.0
		growth0 := int64(-1)
		equalGrowth := true
		badGrowth := false
		for _, f := range snap.TempdbDetail {
			sz := toF(f["size_mb"])
			if sz < min {
				min = sz
			}
			if sz > max {
				max = sz
			}
			gr := toI(f["growth"])
			if growth0 == -1 {
				growth0 = gr
			} else if gr != growth0 {
				equalGrowth = false
			}
			if bval(f["is_percent_growth"]) || gr == 0 {
				badGrowth = true
			}
			sizes = append(sizes, fmt.Sprintf("%s — %.0f МБ", sstr(f["name"]), sz))
			if bval(f["is_percent_growth"]) {
				growths = append(growths, fmt.Sprintf("%s — %d%%", sstr(f["name"]), gr))
			} else {
				growths = append(growths, fmt.Sprintf("%s — %d МБ", sstr(f["name"]), gr*8/1024))
			}
		}
		if len(snap.TempdbDetail) >= 2 && min > 0 && max/min > 1.25 {
			*audit = append(*audit, AuditRow{"Размеры файлов tempdb", "разные", "одинаковые", "warn"})
			add("warning", "Конфигурация", "Файлы tempdb разного размера",
				"Размеры: "+strings.Join(sizes, ", ")+". Пропорциональный алгоритм заполнения даёт "+
					"преимущество крупным файлам — нагрузка ложится неравномерно.",
				"Выровняйте файлы tempdb до одинакового размера (пропорционально, мелкими шагами, "+
					"чтобы не перегрузить систему).", "")
		}
		if !equalGrowth || badGrowth {
			*audit = append(*audit, AuditRow{"Авторост tempdb", strings.Join(growths, ", "),
				"одинаковый фиксированный шаг", "warn"})
			add("warning", "Конфигурация", "Неоптимальный авторост файлов tempdb",
				"Авторост: "+strings.Join(growths, ", ")+". Процентный рост и разный шаг у файлов "+
					"приводят к неравномерному заполнению и паузам при расширении.",
				"Задайте одинаковый фиксированный шаг (256–512 МБ) у всех файлов tempdb и преаллокацию "+
					"вместо роста мелкими шагами.",
				"ALTER DATABASE tempdb MODIFY FILE (NAME = N'<файл>', SIZE = 8GB, FILEGROWTH = 512MB);")
		} else {
			chk("tempdb: файлы равного размера", "ok", "Размер и шаг автороста файлов tempdb выровнены.")
		}
	}

	// ==================================================================
	//  8. Непрямой checkpoint (target_recovery_time)
	// ==================================================================
	var noIndirect []string
	for _, d := range userDatabases(snap) {
		trt := toI(d["target_recovery_time_in_seconds"])
		if trt == 0 {
			noIndirect = append(noIndirect, sstr(d["name"]))
		}
	}
	if len(noIndirect) > 0 {
		heavy := []string{}
		for _, n := range noIndirect {
			if dbSizeMB(snap, n) > 5120 {
				heavy = append(heavy, n)
			}
		}
		*audit = append(*audit, AuditRow{"Непрямой checkpoint (target_recovery_time)",
			strings.Join(noIndirect, ", ") + " — 0 сек", "60 сек", map[bool]string{true: "warn", false: "ok"}[len(heavy) > 0]})
		if len(heavy) > 0 {
			add("warning", "Базы данных", "Непрямой checkpoint выключен (target_recovery_time = 0)",
				"Базы: "+strings.Join(heavy, ", ")+". База, развёрнутая со старой версии SQL Server, "+
					"сохраняет автоматический checkpoint со всплесками записи; с 2016 новые базы создаются "+
					"с непрямым checkpoint и целевым временем восстановления 60 секунд, запись идёт ровным фоном. "+
					"Эффект заметнее всего на машинах с большой памятью.",
				"Включите непрямой checkpoint: ALTER DATABASE ... SET TARGET_RECOVERY_TIME = 60 SECONDS "+
					"(для крупных баз — 60–120 с; учитывайте, что растёт нагрузка фоновой записи).",
				strings.Join(mapStr(heavy, func(n string) string {
					return fmt.Sprintf("ALTER DATABASE [%s] SET TARGET_RECOVERY_TIME = 60 SECONDS;", n)
				}), "\n"))
		}
	}

	// ==================================================================
	//  9. Изоляция транзакций: READ_COMMITTED_SNAPSHOT (RCSI) и ADR
	// ==================================================================
	rcsiOff, legacy := rcsiState(snap)
	if len(rcsiOff) > 0 {
		*audit = append(*audit, AuditRow{"READ_COMMITTED_SNAPSHOT", "выключен: "+strings.Join(rcsiOff, ", "),
			"включён на OLTP-базах", "warn"})
		chk("READ_COMMITTED_SNAPSHOT (RCSI)", "warn", "Читатели ставят разделяемые блокировки и ждут писателей (по умолчанию OFF во всех версиях, включая 2025).")
		ev := "Базы без RCSI: " + strings.Join(rcsiOff, ", ") + ". По умолчанию SQL Server работает в " +
			"READ COMMITTED с блокировками: читающий запрос ждёт пишущую транзакцию и наоборот. " +
			"С RCSI включается версионирование строк — читатели получают снимок данных и не блокируют друг друга. " +
			"Именно поэтому тест «на PostgreSQL 1С не тормозит» сравнивает не СУБД, а две модели изоляции."
		if len(legacy) > 0 {
			ev += " Внимание: у баз " + strings.Join(legacy, ", ") + " старый режим совместимости 1С — " +
				"платформа поднимает уровень изоляции до Repeatable Read/Serializable, и RCSI на такие " +
				"транзакции не действует (флаг меняет только READ COMMITTED). Типичный случай: включили RCSI, " +
				"а LCK_M_* остались."
		}
		fixes := []string{
			"Включите RCSI на OLTP-базах: ALTER DATABASE [<база>] SET READ_COMMITTED_SNAPSHOT ON " +
				"WITH ROLLBACK IMMEDIATE (нужно окно: возможны ожидания).",
			"Следите за tempdb: хранилище версий строк живёт в tempdb; одна длинная транзакция не даёт " +
				"его чистить — версии копятся и tempdb растёт.",
		}
		if len(legacy) > 0 {
			fixes = append(fixes, "Для легаси-конфигураций 1С сначала перевод на управляемый режим "+
				"блокировок (расставить БлокировкаДанных в коде!), затем уровень изоляции, и только потом RCSI. "+
				"Для 1С 8.3 в управляемом режиме (снят режим совместимости с 8.2) платформа включает RCSI сама.")
		}
		add("info", "1С", "Не включён READ_COMMITTED_SNAPSHOT (RCSI)", ev,
			strings.Join(fixes, " "),
			strings.Join(mapStr(rcsiOff, func(n string) string {
				return fmt.Sprintf("ALTER DATABASE [%s] SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE;", n)
			}), "\n"))
	} else if len(snap.Databases) > 0 {
		chk("READ_COMMITTED_SNAPSHOT (RCSI)", "ok", "RCSI включён на рабочих базах.")
	}

	if majorOf(snap) >= 15 {
		var adrOff []string
		for _, d := range userDatabases(snap) {
			if _, ok := d["is_accelerated_database_recovery_on"]; ok && !bval(d["is_accelerated_database_recovery_on"]) {
				adrOff = append(adrOff, sstr(d["name"]))
			}
		}
		if len(adrOff) > 0 {
			add("info", "Базы данных", "Ускоренное восстановление (ADR) выключено",
				"Базы: "+strings.Join(adrOff, ", ")+". ADR ускоряет откат длинных транзакций и восстановление; "+
					"в SQL Server 2025 оптимизированные блокировки работают только при включённом ADR и дают "+
					"наибольшую отдачу в паре с RCSI.",
				"Рассмотрите включение ADR на продуктивных базах (ALTER DATABASE ... SET ACCELERATED_DATABASE_RECOVERY = ON) — "+
					"учитывайте место под хранилище версий и влияние на механику восстановления. Решение принимайте по "+
					"совокупности показателей и лучше с DBA.",
				strings.Join(mapStr(adrOff, func(n string) string {
					return fmt.Sprintf("ALTER DATABASE [%s] SET ACCELERATED_DATABASE_RECOVERY = ON;", n)
				}), "\n"))
		}
	}

	// ==================================================================
	// 10. Усечение журнала (log_reuse_wait)
	// ==================================================================
	reuseNames := map[string]string{
		"LOG_BACKUP": "не сделана копия журнала", "ACTIVE_TRANSACTION": "длительная активная транзакция",
		"REPLICATION": "репликация", "AVAILABILITY_REPLICA": "реплика группы доступности",
		"DATABASE_MIRRORING": "зеркалирование", "CHECKPOINT": "ожидание контрольной точки",
		"BACKUP": "выполняется резервное копирование", "LOG_SCAN": "чтение журнала (log reader/CDC)",
		"OLDEST_PAGE": "старая страница (непрямой checkpoint)",
	}
	var reuse []string
	for _, d := range userDatabases(snap) {
		rw := strings.ToUpper(sstr(d["log_reuse_wait_desc"]))
		if rw == "" || rw == "NOTHING" {
			continue
		}
		reason := reuseNames[rw]
		if reason == "" {
			reason = rw
		}
		reuse = append(reuse, fmt.Sprintf("%s — %s", sstr(d["name"]), reason))
	}
	if len(reuse) > 0 {
		*audit = append(*audit, AuditRow{"Усечение журнала (log_reuse_wait)", strings.Join(reuse, "; "),
			"NOTHING", "warn"})
		add("warning", "Базы данных", "Журнал транзакций не может усечься",
			strings.Join(reuse, "; ")+". Пока причина не устранена, журнал растёт и может занять том "+
				"(аварийный сценарий «журнал переполнен»).",
			"Устраните причину: настройте копии журнала для FULL-модели, не держите длинные транзакции, "+
				"проверьте репликацию/агентов. Не используйте shrink как решение — рвётся цепочка восстановления.", "")
	}

	// ==================================================================
	// 11. Резервные копии: место хранения и шифрование
	// ==================================================================
	var localBkp []string
	anyEncrypted := false
	hasMedia := false
	for _, b := range snap.BackupMedia {
		p := sstr(b["physical_device_name"])
		if p == "" {
			continue
		}
		hasMedia = true
		if sstr(b["encryptor_type"]) != "" && strings.ToUpper(sstr(b["encryptor_type"])) != "NULL" {
			anyEncrypted = true
		}
		if v := volKey(p); v != "" && !strings.HasPrefix(v, `\\`) {
			localBkp = append(localBkp, fmt.Sprintf("%s → %s", sstr(b["db_name"]), p))
		}
	}
	if len(localBkp) > 0 {
		*audit = append(*audit, AuditRow{"Размещение резервных копий", "локальные диски сервера", "сетевой ресурс/вне сервера", "warn"})
		chk("Резервные копии вне сервера", "warn", "Копии хранятся на дисках сервера — при выходе машины из строя теряются и базы, и копии.")
		add("warning", "Резервное копирование", "Резервные копии хранятся на локальных дисках сервера",
			strings.Join(localBkp, "; ")+". При выходе машины из строя теряются и базы, и копии — это "+
				"лишняя точка уязвимости, созданная своими руками. Шифровальщики первым делом ищут именно копии.",
			"Выносите копии на сетевой ресурс, а дополнительную копию — туда, куда по сети не дотянуться "+
				"(съёмные носители, «холодное» облачное хранилище). Незашифрованный .bak восстанавливается "+
				"на любом инстансе — шифруйте копии и храните сертификат отдельно от них.", "")
	}
	if hasMedia && !anyEncrypted {
		*audit = append(*audit, AuditRow{"Шифрование резервных копий", "нет", "включено (сертификат)", "warn"})
		chk("Шифрование резервных копий", "warn", "Копии не шифруются: любой, кто получил доступ к файлу, восстановит «учёт» компании.")
		add("info", "Резервное копирование", "Резервные копии не шифруются",
			"Штатное шифрование копий есть в SQL Server с 2014 (кроме Express) и не требует стороннего софта. "+
				"Незашифрованный .bak на сетевом ресурсе — это полная копия данных компании для каждого, "+
				"у кого есть доступ к папке.",
			"Настройте шифрование. Сертификат и его приватный ключ создаются средствами СУБД и резервируются "+
				"ОТДЕЛЬНО и отдельно от копий: без сертификата зашифрованный архив восстановить невозможно.",
			"USE master;\nCREATE MASTER KEY ENCRYPTION BY PASSWORD = N'<сложный пароль>';\n"+
				"CREATE CERTIFICATE BackupCert WITH SUBJECT = N'Шифрование резервных копий';\n"+
				"BACKUP CERTIFICATE BackupCert TO FILE = N'D:\\Backup\\BackupCert.cer'\n"+
				"  WITH PRIVATE KEY (FILE = N'D:\\Backup\\BackupCert.pvk', ENCRYPTION BY PASSWORD = N'<пароль>');\n"+
				"-- Затем: BACKUP DATABASE [<база>] TO DISK = N'\\\\backup-srv\\share\\<база>.bak'\n"+
				"--   WITH COMPRESSION, CHECKSUM, INIT, ENCRYPTION (ALGORITHM = AES_256, SERVER CERTIFICATE = BackupCert);")
	} else if hasMedia {
		chk("Шифрование резервных копий", "ok", "Обнаружены зашифрованные копии.")
	}

	// ==================================================================
	// 12. Безопасность: смешанная аутентификация, sa, sysadmin-логины
	// ==================================================================
	if len(snap.Security) > 0 {
		windowsOnly := toI(snap.Security["windows_only"])
		sqlLogins := toI(snap.Security["sql_logins"])
		sqlSysadmin := toI(snap.Security["sql_sysadmin"])
		saDisabled, saKnown := snap.Security["sa_disabled"]
		saOn := saKnown && !bval(saDisabled) && sstr(saDisabled) != ""
		if windowsOnly == 0 {
			*audit = append(*audit, AuditRow{"Режим аутентификации", "смешанный (SQL + Windows)", "только Windows", "warn"})
		} else {
			*audit = append(*audit, AuditRow{"Режим аутентификации", "только Windows", "только Windows", "ok"})
		}
		if saOn {
			*audit = append(*audit, AuditRow{"Учётная запись sa", "активна", "отключена", "bad"})
			chk("Учётная запись sa отключена", "warn", "sa включена — известная открытая дыра: полные права, пароль часто лежит в конфиге сервера приложений.")
		} else if saKnown {
			*audit = append(*audit, AuditRow{"Учётная запись sa", "отключена", "отключена", "ok"})
			chk("Учётная запись sa отключена", "ok", "sa отключена.")
		}
		if sqlSysadmin > 0 {
			*audit = append(*audit, AuditRow{"SQL-логины с sysadmin", fmt.Sprintf("%d", sqlSysadmin), "0", "warn"})
		} else if windowsOnly == 0 || sqlLogins > 0 {
			*audit = append(*audit, AuditRow{"SQL-логины с sysadmin", "0", "0", "ok"})
		}
		if windowsOnly == 0 && (saOn || sqlSysadmin > 0) {
			names := ""
			if len(snap.SysLogins) > 0 {
				var ns []string
				for i, l := range snap.SysLogins {
					if i >= 10 {
						break
					}
					ns = append(ns, sstr(l["name"]))
				}
				names = " Привилегированные логины: " + strings.Join(ns, ", ") + "."
			}
			add("warning", "Безопасность", "Смешанная аутентификация и учётные записи с полными правами",
				fmt.Sprintf("Сервер разрешает SQL-аутентификацию; SQL-логинов: %d, из них с правами sysadmin: %d.%s "+
					"Схема «везде sa, под ним всё работает» — открытая дыра: пароль известен неопределённому кругу лиц "+
					"и годами не меняется, а ротация служебных учёток даётся тяжелее всего.", sqlLogins, sqlSysadmin, names),
				"Службы и подключение сервера приложений 1С — под доменными учётки (вершина — gMSA: пароль "+
					"генерирует и ротирует домен), доступ к СУБД — Windows-аутентификация, права выдаются под задачу. "+
					"Отключите sa и удалите ненужные SQL-логины.",
				"ALTER LOGIN [sa] DISABLE;\n-- проверить привилегированных:\n"+
					"SELECT p.name, p.type_desc FROM sys.server_principals p\n"+
					"  WHERE IS_SRVROLEMEMBER('sysadmin', p.name) = 1;")
		}
		if windowsOnly == 0 && sqlSysadmin == 0 && !saOn {
			chk("Режим аутентификации", "manual",
				"Разрешена SQL-аутентификация — убедитесь, что сервер приложений 1С ходит под доменной учётной записью.")
		}
	}

	// Протоколы: TCP/IP должен быть включён, именованные каналы обычно не нужны.
	if v, ok := snap.NetConfig["tcp_enabled"]; ok {
		on := strings.Contains(strings.ToUpper(v), "YES") || v == "1" || strings.EqualFold(v, "TRUE")
		st := "ok"
		if !on {
			st = "bad"
		}
		*audit = append(*audit, AuditRow{"Сетевой протокол TCP/IP", map[bool]string{true: "включён", false: "выключен"}[on], "включён", st})
		if !on {
			add("warning", "Безопасность", "Сетевой протокол TCP/IP выключен",
				"Сервер 1С подключается к СУБД по TCP/IP — с выключенным протоколом связь идёт через именованные каналы или рвётся.",
				"Включите TCP/IP в конфигурации SQL Server, задайте статический порт и согласуйте его с сервером приложений.", "")
		}
	}
	if v, ok := snap.NetConfig["named_pipes_enabled"]; ok {
		on := strings.Contains(strings.ToUpper(v), "YES") || v == "1" || strings.EqualFold(v, "TRUE")
		st := "ok"
		if on {
			st = "warn"
		}
		*audit = append(*audit, AuditRow{"Именованные каналы", map[bool]string{true: "включены", false: "выключены"}[on], "выключены", st})
		if on {
			add("info", "Безопасность", "Включены именованные каналы",
				"Именованные каналы нужны не всегда; лишний протокол — лишняя поверхность атаки (в методике — оставить TCP/IP).",
				"Отключите именованные каналы, если под них ничего не написано. Проверьте также правило брандмауэра: "+
					"порт 1433 разрешается не «из локальной сети», а только с адреса сервера приложений 1С; SQL Browser "+
					"выключается, порт инстанса задаётся статически; RDP наружу не публикуется (VPN).", "")
		}
	}

	// ==================================================================
	// 13. Мониторинг и профиль нагрузки (1С)
	// ==================================================================
	if majorOf(snap) >= 13 {
		var qsOff []string
		qsReadOnly := []string{}
		for _, d := range userDatabases(snap) {
			if v, ok := d["is_query_store_on"]; ok && !bval(v) {
				qsOff = append(qsOff, sstr(d["name"]))
			}
		}
		for _, q := range snap.QueryStore {
			if strings.EqualFold(sstr(q["actual_state_desc"]), "READ_ONLY") {
				qsReadOnly = append(qsReadOnly, fmt.Sprintf("%s (причина: %s)",
					sstr(q["db_name"]), orDash(sstr(q["readonly_reason"]))))
			}
		}
		if len(qsOff) > 0 {
			*audit = append(*audit, AuditRow{"Query Store", "выключен: " + strings.Join(qsOff, ", "), "включён", "warn"})
			chk("Query Store включён", "warn", "Без Query Store история планов и ожиданий по запросам не копится.")
			add("info", "Мониторинг", "Query Store не включён на части баз",
				"Базы: "+strings.Join(qsOff, ", ")+". С 2017 Query Store хранит и статистику ожиданий в привязке "+
					"к конкретным запросам, с 2022 включён по умолчанию для новых баз; база, восстановленная со "+
					"старой версии, сохраняет прежние настройки — включать нужно вручную. Данные копятся сами и "+
					"ждут того, кто сможет их прочитать.",
				"Включите и настройте Query Store на продуктивных базах; мониторинг имеет смысл только тогда, "+
					"когда есть кому читать метрики (штатный DBA или внешний специалист на сопровождении).",
				strings.Join(mapStr(qsOff, func(n string) string {
					return fmt.Sprintf("ALTER DATABASE [%s] SET QUERY_STORE = ON\n"+
						"  (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = AUTO,\n"+
						"   DATA_FLUSH_INTERVAL_SECONDS = 900, INTERVAL_LENGTH_MINUTES = 15,\n"+
						"   MAX_STORAGE_SIZE_MB = 1024);", n)
				}), "\n"))
		} else if len(snap.QueryStore) > 0 {
			chk("Query Store включён", "ok", "Query Store включён на рабочих базах.")
		}
		if len(qsReadOnly) > 0 {
			*audit = append(*audit, AuditRow{"Query Store (состояние)", strings.Join(qsReadOnly, "; "), "READ_WRITE", "warn"})
			add("warning", "Мониторинг", "Query Store переведён в режим READ_ONLY",
				strings.Join(qsReadOnly, "; ")+". Чаще всего причина — достигнут лимит MAX_STORAGE_SIZE_MB: "+
					"сбор статистики прекращается.",
				"Увеличьте MAX_STORAGE_SIZE_MB и верните OPERATION_MODE = READ_WRITE.",
				"ALTER DATABASE [<база>] SET QUERY_STORE (MAX_STORAGE_SIZE_MB = 2048, OPERATION_MODE = READ_WRITE);")
		}
	}

	// Профиль нагрузки и его следствия (две принципиально разные конфигурации из методики).
	var heaviest string
	var heaviestGB float64
	dbCount := 0
	for _, d := range userDatabases(snap) {
		dbCount++
		gb := dbSizeMB(snap, sstr(d["name"])) / 1024
		if gb > heaviestGB {
			heaviestGB, heaviest = gb, sstr(d["name"])
		}
	}
	if dbCount >= 30 {
		*audit = append(*audit, AuditRow{"Профиль нагрузки", fmt.Sprintf("куст баз (%d)", dbCount), "план обслуживания по всем базам", "warn"})
		chk("Профиль нагрузки", "manual", fmt.Sprintf("Куст баз (%d): главное — план обслуживания, память на всех и раздельное восстановление.", dbCount))
		add("info", "1С", fmt.Sprintf("Профиль нагрузки: куст баз (%d баз на инстансе)", dbCount),
			"Много небольших баз: каждая нужна редко, но в период отчётности нагрузка растёт по всем сразу. "+
				"Борьба идёт за план обслуживания, который успевает пройти по всем базам за ночь, за память, "+
				"поделённую на всех, и за возможность восстановить одну базу, не трогая остальные (у каждой базы "+
				"свой журнал и своё восстановление на момент времени).",
			"Проверьте план обслуживания (индексы, статистики, DBCC, бэкапы) и его укладывание в ночное окно; "+
				"следите за свободной памятью и tempdb; убедитесь, что восстановление одной базы не блокирует остальные.", "")
	} else if heaviestGB >= 50 && heaviest != "" {
		*audit = append(*audit, AuditRow{"Профиль нагрузки", fmt.Sprintf("тяжёлая ERP (%s, %.0f ГБ)", heaviest, heaviestGB),
			"параллелизм, tempdb, блокировки", "ok"})
		chk("Профиль нагрузки", "manual", fmt.Sprintf("Тяжёлая ERP (%s, %.0f ГБ): главное — параллелизм, tempdb и блокировки.", heaviest, heaviestGB))
		add("info", "1С", fmt.Sprintf("Профиль нагрузки: тяжёлая ERP (%s, %.0f ГБ)", heaviest, heaviestGB),
			"Одна большая база, сотни активных пользователей, длинные транзакции и тяжёлые отчёты поверх "+
				"оперативной работы. Для этого профиля главная борьба идёт за параллелизм, tempdb и блокировки.",
			"Проверяйте MAXDOP и cost threshold по фактическим ожиданиям (настройка занимает недели), "+
				"держите tempdb с несколькими файлами равного размера на быстром томе, контролируйте "+
				"блокировки и длину транзакций.", "")
	}

	// ==================================================================
	// 14. Постоянный чек-лист: что проверить руками (методика)
	// ==================================================================
	chk("Антивирус: исключения для SQL Server", "manual",
		"Внесите в исключения расширения mdf, ndf, ldf, bak, trn, каталоги СУБД и процесс sqlservr.exe — "+
			"иначе сканер проверяет файлы на каждом обращении и сервер делает работу дважды.")
	chk("Кластер томов 64 КБ", "manual",
		"Тома под данные, журналы и tempdb форматируются кластером 64 КБ (SQL Server работает экстентами по 64 КБ).")
	chk("Эталон производительности дисков (DiskSpd)", "manual",
		"Прогоните синтетический тест (DiskSpd) до продакшена: цифры нужны при приёмке железа и как эталон "+
			"для сравнения при подозрениях на деградацию накопителей.")
	chk("Сеть и доступы к серверу СУБД", "manual",
		"Порт 1433 разрешён только с адреса сервера приложений 1С, SQL Browser (UDP 1434) выключен, "+
			"RDP наружу не публикуется (VPN для админов и подрядчиков), бэкапы не лежат в доменной шаре с доступом всем.")
	chk("Ответственный за мониторинг", "manual",
		"Мониторинг работает только тогда, когда есть кому разбирать метрики: штатный DBA или внешний "+
			"специалист на сопровождении. Алерты должны приходить тому, кто умеет их читать.")
	chk("История ожиданий и эталонный снимок", "manual",
		"Копите историю типов ожиданий и основных счётчиков и сохраните эталонный снимок из спокойного "+
			"периода: сравнение системы идёт с её же прошлым, а не с универсальными нормативами.")
	chk("Lock Pages in Memory", "manual",
		"Для крупных серверов выдайте служебной учётке право блокировки страниц в памяти, чтобы буферный пул "+
			"не вытеснялся в файл подкачки.")
	chk("Службы и подключения 1С", "manual",
		"Сервер приложений 1С и СУБД — на разных машинах; рабочие каталоги и Temp сервера 1С вынесены "+
			"на быстрые отдельные диски (NVMe): поток временных файлов конкурирует с СУБД за диск.")
}

// maxServerMemoryMB — текущее ограничение памяти SQL Server (0, если не задано/не прочитано).
func maxServerMemoryMB(snap *Snapshot) int64 {
	if v, ok := snap.Config["max server memory (MB)"]; ok {
		n := toI(v)
		if n >= 2147483647 {
			return 0
		}
		return n
	}
	return 0
}

// majorOf — основной номер версии экземпляра (15 = SQL Server 2019), 0 если неизвестен.
func majorOf(snap *Snapshot) int {
	return productMajor(snap.Instance["product_version"])
}

// addJoin склеивает список для текста наблюдений.
func addJoin(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "На одном томе: " + strings.Join(items, "; ")
}

