// 数据库报错翻成人话 —— 只覆盖最常见的约束/类型错误, 其余原样返回(不吞原始信息)。
// 为什么需要: 直接把 MySQL/Oracle 原文丢给用户, 用户看到的是代码; 而这几类恰恰是
// "用户操作意图与表约束冲突", 说清是哪一类冲突比编号有用得多(编号仍保留在末尾便于排查)。
export function humanizeDbError(raw: string): string {
  const s = String(raw || '')
  const code = (n: number) => new RegExp(`Error ${n}\\b`).test(s)
  const tail = match => (match ? s.match(new RegExp(`Error \\d+[^)]*\\)?[^\\n]{0,120}`))?.[0] : '')

  if (code(1062)) {
    const e = /Duplicate entry '([^']*)' for key '([^']*)'/.exec(s)
    const key = e?.[2] || '唯一索引'
    return `唯一键冲突：${e?.[1] ? `「${e[1]}」` : '该键值'} 在 ${key} 里已经存在 —— 想新增记录请用「插入行」，不要改现有行的键值`
  }
  if (code(1452)) {
    // Cannot add or update a child row: a foreign key constraint fails (`db`.`child`, CONSTRAINT `c` FOREIGN KEY (`col`) REFERENCES `parent` (`pcol`) ...)
    const e = /FOREIGN KEY \(`?([^`)]+)`?\) REFERENCES `?([^`(]+)`? \(`?([^`)]+)`?\)/.exec(s)
    return `外键约束：${e?.[1] || '该字段'} 的值必须在 ${e?.[2] || '父表'}.${e?.[3] || '主键'} 里已经存在`
  }
  if (code(1451)) return '外键约束：这行还被其它表引用，不能修改/删除'
  if (code(1048)) {
    const e = /Column '([^']+)' cannot be null/.exec(s)
    return `字段 ${e?.[1] || ''} 不允许为空（该列是 NOT NULL）`
  }
  if (code(1406)) {
    const e = /Data too long for column '([^']+)'/.exec(s)
    return `字段 ${e?.[1] || ''} 的内容超出该列长度`
  }
  if (code(1264) || code(1366) || code(1292)) return '值的类型/范围与该列不匹配'
  if (code(3819)) return '不满足该表的检查约束（CHECK）'
  void tail
  return s
}
