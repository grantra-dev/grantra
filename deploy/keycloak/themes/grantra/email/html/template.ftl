<#macro emailLayout>
<html lang="${locale.language}" dir="${(ltr)?then('ltr','rtl')}">
<head><style>a { color: #4338ca; font-weight: 600; }</style></head>
<body style="margin:0;padding:24px;background:#f6f7fb;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#0f172a;">
  <table role="presentation" width="100%" cellspacing="0" cellpadding="0">
    <tr><td align="center">
      <table role="presentation" width="560" cellspacing="0" cellpadding="0" style="max-width:560px;width:100%;background:#ffffff;border:1px solid #e2e8f0;border-top:4px solid #4338ca;border-radius:8px;">
        <tr><td style="padding:24px 28px 0;font-size:18px;font-weight:600;color:#4338ca;">Grantra Login</td></tr>
        <tr><td style="padding:16px 28px 28px;font-size:15px;line-height:1.6;">
          <#nested>
        </td></tr>
      </table>
      <p style="margin:16px 0 0;font-size:12px;color:#475569;">Sent by ${realmName!"Grantra Login"}</p>
    </td></tr>
  </table>
</body>
</html>
</#macro>
