FROM centos:7.9.2009
# 设 置 工 作 目 录
WORKDIR /app
# 复 制 二 进 制 文 件
COPY traffic-checker .
# 暴 露 端 口
EXPOSE 8080
# 运 行 应 用
CMD ["./traffic-checker"]