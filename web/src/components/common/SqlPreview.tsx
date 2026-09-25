// 落库前的 SQL 预览内容块(L2 公共层): 弹窗编辑保存、置 NULL、删除行、结构变更共用同一个"将要执行什么"的呈现。
// 后端(confirm=false)已生成好方言化的语句, 这里只负责"看得清、选得中、复制得走、撑不爆"。

// 有界预览(dbx DangerConfirmDialog 同款语义): 预览是给人看的, 不是给人下载的 ——
// 一条几十万字符的 DELETE(或几百条 ALTER)直接灌进 DOM, 确认弹窗会卡到点不动。头尾各留一半,
// 中间折叠成一行提示; 「复制」按钮始终给全文。行数与字符数谁先超就按谁切。
const PREVIEW_MAX_CHARS = 8192
const PREVIEW_MAX_LINES = 200

interface BoundedPreview {
  head: string
  tail: string
  truncated: boolean
  omittedLines: number
  omittedChars: number
}

// 切点落在代理对中间会切出半个字符(字符串字面量里的 emoji 就是这种情况), 两头各让一位
function clampEnd(text: string, end: number): number {
  const prev = text.charCodeAt(end - 1)
  return prev >= 0xd800 && prev <= 0xdbff ? end - 1 : end
}
function clampStart(text: string, start: number): number {
  const cur = text.charCodeAt(start)
  return cur >= 0xdc00 && cur <= 0xdfff ? start + 1 : start
}

export function boundedSqlPreview(text: string): BoundedPreview {
  const lines = text.split('\n')
  if (text.length <= PREVIEW_MAX_CHARS && lines.length <= PREVIEW_MAX_LINES) {
    return { head: text, tail: '', truncated: false, omittedLines: 0, omittedChars: 0 }
  }
  const halfLines = Math.floor(PREVIEW_MAX_LINES / 2)
  const halfChars = Math.floor(PREVIEW_MAX_CHARS / 2)
  const byLinesHead = lines.slice(0, halfLines).join('\n').length
  const byLinesTail = text.length - lines.slice(-halfLines).join('\n').length
  const headEnd = clampEnd(text, Math.min(byLinesHead, halfChars))
  const tailStart = clampStart(text, Math.max(headEnd, byLinesTail, text.length - halfChars))
  const head = text.slice(0, headEnd)
  const tail = text.slice(tailStart)
  const visibleLines = head.split('\n').length + (tail ? tail.split('\n').length : 0)
  return {
    head,
    tail,
    truncated: true,
    omittedLines: Math.max(0, lines.length - visibleLines),
    omittedChars: tailStart - headEnd,
  }
}

export function SqlPreviewBody({ sqls, caption = '主键定位' }: { sqls: string[]; caption?: string }) {
  const copy = () => { navigator.clipboard?.writeText(sqls.join(';\n')) }
  const preview = boundedSqlPreview(sqls.join(';\n'))
  return (
    <div className="db-sql-preview">
      <div className="db-sql-preview-head">
        <span className="dim">{sqls.length} 条语句 · {caption}</span>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={copy}>复制</button>
      </div>
      {preview.truncated ? (
        <>
          <pre className="db-sql-preview-sql">{preview.head}</pre>
          <div className="db-sql-preview-omitted">
            已省略 {preview.omittedLines} 行 / {preview.omittedChars} 字符(「复制」仍是全文)
          </div>
          <pre className="db-sql-preview-sql">{preview.tail}</pre>
        </>
      ) : (
        sqls.map((s, i) => <pre key={i} className="db-sql-preview-sql">{s}</pre>)
      )}
    </div>
  )
}
