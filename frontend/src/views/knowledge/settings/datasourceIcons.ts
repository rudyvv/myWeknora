import feishuIcon from '@/assets/img/datasource-feishu.ico'
import gitlabIcon from '@/assets/img/datasource-gitlab.png'
import larkIcon from '@/assets/img/datasource-lark.svg'
import notionIcon from '@/assets/img/datasource-notion.ico'
import yuqueIcon from '@/assets/img/datasource-yuque.ico'
import rssIcon from '@/assets/img/datasource-rss.svg'
import dingtalkIcon from '@/assets/img/im/dingtalk.svg'
import wecomIcon from '@/assets/img/im/wecom.svg'
import imaIcon from '@/assets/img/datasource-ima.png'

export const datasourceIconMap: Record<string, string> = {
  feishu: feishuIcon,
  lark: larkIcon,
  // Drive (云盘) connectors reuse the wiki icons - same product, same brand.
  feishu_drive: feishuIcon,
  lark_drive: larkIcon,
  notion: notionIcon,
  yuque: yuqueIcon,
  dingtalk: dingtalkIcon,
  rss: rssIcon,
  gitlab: gitlabIcon,
  ima: imaIcon,
  wecom_drive_rpa: wecomIcon,
}

export function getDatasourceIconUrl(type: string): string | undefined {
  return datasourceIconMap[type]
}
