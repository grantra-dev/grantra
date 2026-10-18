# Privileged access

Some access is powerful enough that Grantra hands it out for a short time and records what happens with it. You only see the pages below when you hold access to something in them.

## Just in time

Privileged roles are often given as *eligible* rather than for good. To use one, open **My access**, choose **Activate**, give a reason and how many hours you need. Some roles need an approval first. The access ends on its own when the time is up, or earlier if you end it.

## Vault

**Vault** holds the passwords and keys of shared and administrator accounts. Check one out with a reason, use it, and check it in when you are done. The password may be changed after you use it, so do not save it anywhere.

**Break glass** is for emergencies, when the usual way in does not work. Only the people chosen as responders see it. Every use alerts the admins and is reviewed afterwards.

## Servers

**Servers** lists the SSH servers you hold access to. Paste your *public* SSH key to get a short-lived certificate and the command to connect. The certificate only works while your access lasts. Sessions go through Grantra's gateway and are recorded.

## Remote access

**Remote access** lists the databases, Kubernetes clusters, Windows desktops and cloud consoles you hold access to:

- **Databases and clusters**: get a token and the command to connect (a kubeconfig for a cluster). The token lasts as long as your access.
- **Desktops** open in a new browser tab.
- **Cloud consoles** (AWS, Azure, Google Cloud) open signed in, with command line credentials on AWS.

You never see the real password or key. Queries, cluster requests and desktop sessions are recorded, and a session ends when your access does.
